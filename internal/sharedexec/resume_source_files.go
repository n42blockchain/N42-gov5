package sharedexec

import (
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"github.com/n42blockchain/N42/common/types"
)

func resumeSourcePrivateRoot(path string) error {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || !filepath.IsAbs(path) || filepath.Clean(path) != path || resolved != path {
		return errors.New("resume source root must exist without aliases")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !ok || owner.Uid != uint32(os.Geteuid()) || info.Mode().Perm() != 0700 || !info.IsDir() {
		return errors.New("resume source root must be private owned directory")
	}
	return nil
}

func resumeSourceStat(path string, info os.FileInfo) (ResumeSourceMetadata, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || stat.Uid != uint32(os.Geteuid()) || stat.Nlink != 1 || info.Size() <= 0 {
		return ResumeSourceMetadata{}, errors.New("resume source requires owned regular 0600 single-link files")
	}
	return ResumeSourceMetadata{Path: path, Device: uint64(stat.Dev), Inode: stat.Ino, Bytes: uint64(info.Size()), Links: stat.Nlink, UID: stat.Uid, Mode: uint32(info.Mode()), ModifiedNS: info.ModTime().UnixNano(), ChangedNS: stat.Ctim.Sec*1e9 + stat.Ctim.Nsec}, nil
}

func checkResumeSources(files []resumeSourceFile, before []ResumeSourceMetadata) ([]ResumeSourceMetadata, error) {
	if len(files) != len(before) {
		return nil, errors.New("resume source descriptor set changed")
	}
	after := make([]ResumeSourceMetadata, len(files))
	for i, source := range files {
		if err := checkResumeSourceFile(source); err != nil {
			return nil, err
		}
		fdInfo, err := source.file.Stat()
		if err != nil {
			return nil, err
		}
		fd, err := resumeSourceStat(before[i].Path, fdInfo)
		if err != nil {
			return nil, err
		}
		pathInfo, err := os.Lstat(before[i].Path)
		if err != nil {
			return nil, err
		}
		path, err := resumeSourceStat(before[i].Path, pathInfo)
		if err != nil || fd != before[i] || path != before[i] {
			return nil, errors.New("resume source path/descriptor metadata changed")
		}
		after[i] = path
	}
	return after, nil
}

type resumeSourceFile struct {
	file *os.File
	info os.FileInfo
}

func closeResumeSources(files []resumeSourceFile) {
	for _, file := range files {
		if file.file != nil {
			_ = file.file.Close()
		}
	}
}

// A timed prefix cannot use the raw sink's EOF proof. Hash every complete shard
// before dispatch, seek the same open descriptors, and retain those descriptors.
func openResumeSources(ctx context.Context, root string, m CorpusManifest) (files []resumeSourceFile, err error) {
	defer func() {
		if err != nil {
			closeResumeSources(files)
		}
	}()
	buffer := make([]byte, 8<<20)
	for _, shard := range m.Shards {
		path := filepath.Join(root, shard.File)
		before, e := os.Lstat(path)
		if e != nil {
			return files, e
		}
		owner, ok := before.Sys().(*syscall.Stat_t)
		if !ok || owner.Uid != uint32(os.Geteuid()) || !before.Mode().IsRegular() || before.Mode().Perm() != 0600 || before.Size() != int64(shard.Bytes) {
			return files, errors.New("strict source must be an owned private regular file of exact size")
		}
		file, e := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
		if e != nil {
			return files, e
		}
		files = append(files, resumeSourceFile{file: file, info: before})
		after, e := file.Stat()
		if e != nil {
			return files, e
		}
		if !os.SameFile(before, after) {
			return files, errors.New("strict source changed while opening")
		}
		hash := sha256.New()
		var n int64
		for {
			if e := ctx.Err(); e != nil {
				return files, e
			}
			count, readErr := file.Read(buffer)
			if count > 0 {
				hash.Write(buffer[:count])
				n += int64(count)
			}
			if readErr == io.EOF {
				break
			}
			if readErr != nil {
				return files, readErr
			}
			if count == 0 {
				return files, io.ErrNoProgress
			}
		}
		if n != int64(shard.Bytes) || types.BytesToHash(hash.Sum(nil)) != shard.SHA256 {
			return files, errors.New("strict full source shard integrity failed")
		}
		if _, e := file.Seek(0, io.SeekStart); e != nil {
			return files, e
		}
		if e := checkResumeSourceFile(files[len(files)-1]); e != nil {
			return files, e
		}
	}
	return files, nil
}
func checkResumeSourceFile(source resumeSourceFile) error {
	after, err := source.file.Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(source.info, after) || source.info.Size() != after.Size() || !source.info.ModTime().Equal(after.ModTime()) || after.Mode().Perm() != 0600 {
		return errors.New("strict source file changed after integrity check")
	}
	return nil
}
