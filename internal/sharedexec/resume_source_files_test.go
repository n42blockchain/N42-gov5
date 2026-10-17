package sharedexec

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestResumeSourceMetadataRejectsAliasesAndRestoredModificationTime(t *testing.T) {
	for _, name := range []string{"path replaced", "symlink", "hardlink", "write restored mtime"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "source.gcf")
			raw := []byte("private metadata identity fixture")
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			info, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			before, err := resumeSourceStat(path, info)
			if err != nil {
				t.Fatal(err)
			}
			file, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			files := []resumeSourceFile{{file: file, info: info}}
			switch name {
			case "path replaced", "symlink":
				saved := path + ".saved"
				if err := os.Rename(path, saved); err != nil {
					t.Fatal(err)
				}
				if name == "symlink" {
					err = os.Symlink(saved, path)
				} else {
					err = os.WriteFile(path, raw, 0600)
				}
				if err != nil {
					t.Fatal(err)
				}
			case "hardlink":
				if err := os.Link(path, path+".alias"); err != nil {
					t.Fatal(err)
				}
			case "write restored mtime":
				time.Sleep(2 * time.Millisecond)
				if err := os.WriteFile(path, raw, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := checkResumeSources(files, []ResumeSourceMetadata{before}); err == nil {
				t.Fatal("changed path/ctime/link identity accepted")
			}
		})
	}
}
