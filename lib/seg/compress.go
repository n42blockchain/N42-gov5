/*
   Copyright 2021 Erigon contributors

   Licensed under the Apache License, Version 2.0 (the "License");
   you may not use this file except in compliance with the License.
   You may obtain a copy of the License at

       http://www.apache.org/licenses/LICENSE-2.0

   Unless required by applicable law or agreed to in writing, software
   distributed under the License is distributed on an "AS IS" BASIS,
   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   See the License for the specific language governing permissions and
   limitations under the License.
*/

package seg

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	dir2 "github.com/n42blockchain/N42/lib/common/dir"
	"github.com/n42blockchain/N42/lib/etl"
	"github.com/n42blockchain/N42/lib/log/v3"
)

// Compressor is the main operating type for performing per-word compression
// After creating a compression, one needs to add superstrings to it, using `AddWord` function
// In order to add word without compression, function `AddUncompressedWord` needs to be used
// Compressor only tracks which words are compressed and which are not until the compressed
// file is created. After that, the user of the file needs to know when to call
// `Next` or `NextUncompressed` function on the decompressor.
// After that, `Compress` function needs to be called to perform the compression
// and eventually create output file
type Compressor struct {
	ctx              context.Context
	wg               *sync.WaitGroup
	superstrings     chan []byte
	uncompressedFile *RawWordsFile
	tmpDir           string // temporary directory to use for ETL when building dictionary
	logPrefix        string
	outputFile       string // File where to output the dictionary and compressed data
	tmpOutFilePath   string // File where to output the dictionary and compressed data
	suffixCollectors []*etl.Collector
	// Buffer for "superstring" - transformation of superstrings where each byte of a word, say b,
	// is turned into 2 bytes, 0x01 and b, and two zero bytes 0x00 0x00 are inserted after each word
	// this is needed for using ordinary (one string) suffix sorting algorithm instead of a generalised (many superstrings) suffix
	// sorting algorithm
	superstring      []byte
	wordsCount       uint64
	superstringCount uint64
	superstringLen   int
	workers          int
	Ratio            CompressionRatio
	lvl              log.Lvl
	trace            bool
	logger           log.Logger
	noFsync          bool // fsync is enabled by default, but tests can manually disable
}

func NewCompressor(ctx context.Context, logPrefix, outputFile, tmpDir string, minPatternScore uint64, workers int, lvl log.Lvl, logger log.Logger) (*Compressor, error) {
	dir2.MustExist(tmpDir)
	dir, fileName := filepath.Split(outputFile)

	// tmpOutFilePath is a ".seg.tmp" file which will be renamed to ".seg" if everything succeeds.
	// It allows to atomically create a ".seg" file (the downloader will not see partial ".seg" files).
	tmpOutFilePath := filepath.Join(dir, fileName) + ".tmp"

	uncompressedPath := filepath.Join(tmpDir, fileName) + ".idt"
	uncompressedFile, err := NewRawWordsFile(uncompressedPath)
	if err != nil {
		return nil, err
	}

	// Collector for dictionary superstrings (sorted by their score)
	superstrings := make(chan []byte, workers*2)
	wg := &sync.WaitGroup{}
	wg.Add(workers)
	suffixCollectors := make([]*etl.Collector, workers)
	for i := 0; i < workers; i++ {
		collector := etl.NewCollector(logPrefix+"_dict", tmpDir, etl.NewSortableBuffer(etl.BufferOptimalSize/2), logger) //nolint:gocritic
		collector.LogLvl(lvl)

		suffixCollectors[i] = collector
		go extractPatternsInSuperstrings(ctx, superstrings, collector, minPatternScore, wg, logger)
	}

	return &Compressor{
		uncompressedFile: uncompressedFile,
		tmpOutFilePath:   tmpOutFilePath,
		outputFile:       outputFile,
		tmpDir:           tmpDir,
		logPrefix:        logPrefix,
		workers:          workers,
		ctx:              ctx,
		superstrings:     superstrings,
		suffixCollectors: suffixCollectors,
		lvl:              lvl,
		wg:               wg,
		logger:           logger,
	}, nil
}

func (c *Compressor) Close() {
	c.uncompressedFile.CloseAndRemove()
	for _, collector := range c.suffixCollectors {
		collector.Close()
	}
	c.suffixCollectors = nil
}

func (c *Compressor) SetTrace(trace bool) { c.trace = trace }
func (c *Compressor) Workers() int        { return c.workers }

func (c *Compressor) Count() int { return int(c.wordsCount) }

func (c *Compressor) AddWord(word []byte) error {
	select {
	case <-c.ctx.Done():
		return c.ctx.Err()
	default:
	}

	c.wordsCount++
	l := 2*len(word) + 2
	if c.superstringLen+l > superstringLimit {
		if c.superstringCount%samplingFactor == 0 {
			c.superstrings <- c.superstring
		}
		c.superstringCount++
		c.superstring = make([]byte, 0, 1024*1024)
		c.superstringLen = 0
	}
	c.superstringLen += l

	if c.superstringCount%samplingFactor == 0 {
		for _, a := range word {
			c.superstring = append(c.superstring, 1, a)
		}
		c.superstring = append(c.superstring, 0, 0)
	}

	return c.uncompressedFile.Append(word)
}

func (c *Compressor) AddUncompressedWord(word []byte) error {
	select {
	case <-c.ctx.Done():
		return c.ctx.Err()
	default:
	}

	c.wordsCount++
	return c.uncompressedFile.AppendUncompressed(word)
}

func (c *Compressor) Compress() error {
	if err := c.uncompressedFile.Flush(); err != nil {
		return err
	}

	logEvery := time.NewTicker(20 * time.Second)
	defer logEvery.Stop()
	if len(c.superstring) > 0 {
		c.superstrings <- c.superstring
	}
	close(c.superstrings)
	c.wg.Wait()

	if c.lvl < log.LvlTrace {
		c.logger.Log(c.lvl, fmt.Sprintf("[%s] BuildDict start", c.logPrefix), "workers", c.workers)
	}
	t := time.Now()
	db, err := DictionaryBuilderFromCollectors(c.ctx, compressLogPrefix, c.tmpDir, c.suffixCollectors, c.lvl, c.logger)
	if err != nil {
		return err
	}
	if c.trace {
		_, fileName := filepath.Split(c.outputFile)
		if err := PersistDictionary(filepath.Join(c.tmpDir, fileName)+".dictionary.txt", db); err != nil {
			return err
		}
	}
	defer os.Remove(c.tmpOutFilePath)
	if c.lvl < log.LvlTrace {
		c.logger.Log(c.lvl, fmt.Sprintf("[%s] BuildDict", c.logPrefix), "took", time.Since(t))
	}

	cf, err := os.Create(c.tmpOutFilePath)
	if err != nil {
		return err
	}
	defer cf.Close()
	t = time.Now()
	if err := compressWithPatternCandidates(c.ctx, c.trace, c.logPrefix, c.tmpOutFilePath, cf, c.uncompressedFile, c.workers, db, c.lvl, c.logger); err != nil {
		return err
	}
	if err = c.fsync(cf); err != nil {
		return err
	}
	if err = cf.Close(); err != nil {
		return err
	}
	if err := os.Rename(c.tmpOutFilePath, c.outputFile); err != nil {
		return fmt.Errorf("renaming: %w", err)
	}

	c.Ratio, err = Ratio(c.uncompressedFile.filePath, c.outputFile)
	if err != nil {
		return fmt.Errorf("ratio: %w", err)
	}

	_, fName := filepath.Split(c.outputFile)
	if c.lvl < log.LvlTrace {
		c.logger.Log(c.lvl, fmt.Sprintf("[%s] Compress", c.logPrefix), "took", time.Since(t), "ratio", c.Ratio, "file", fName)
	}
	return nil
}

func (c *Compressor) DisableFsync() { c.noFsync = true }

// fsync - other processes/goroutines must see only "fully-complete" (valid) files. No partial-writes.
// To achieve it: write to .tmp file then `rename` when file is ready.
// Machine may power-off right after `rename` - it means `fsync` must be before `rename`
func (c *Compressor) fsync(f *os.File) error {
	if c.noFsync {
		return nil
	}
	if err := f.Sync(); err != nil {
		c.logger.Warn("couldn't fsync", "err", err, "file", c.tmpOutFilePath)
		return err
	}
	return nil
}

// superstringLimit limits how large can one "superstring" get before it is processed
// CompressorSequential allocates 7 bytes for each uint of superstringLimit. For example,
// superstingLimit 16m will result in 112Mb being allocated for various arrays
const superstringLimit = 16 * 1024 * 1024

// minPatternLen is minimum length of pattern we consider to be included into the dictionary
const minPatternLen = 5
const maxPatternLen = 128

// maxDictPatterns is the maximum number of patterns allowed in the initial (not reduced dictionary)
// Large values increase memory consumption of dictionary reduction phase
/*
Experiments on 74Gb uncompressed file (bsc 012500-013000-transactions.seg)
Ram - needed just to open compressed file (Huff tables, etc...)
dec_speed - loop with `word, _ = g.Next(word[:0])`
skip_speed - loop with `g.Skip()`
| DictSize | Ram  | file_size | dec_speed | skip_speed |
| -------- | ---- | --------- | --------- | ---------- |
| 1M       | 70Mb | 35871Mb   | 4m06s     | 1m58s      |
| 512K     | 42Mb | 36496Mb   | 3m49s     | 1m51s      |
| 256K     | 21Mb | 37100Mb   | 3m44s     | 1m48s      |
| 128K     | 11Mb | 37782Mb   | 3m25s     | 1m44s      |
| 64K      | 7Mb  | 38597Mb   | 3m16s     | 1m34s      |
| 32K      | 5Mb  | 39626Mb   | 3m0s      | 1m29s      |

*/
const maxDictPatterns = 64 * 1024

// samplingFactor - skip superstrings if `superstringNumber % samplingFactor != 0`
const samplingFactor = 4

// nolint
const compressLogPrefix = "compress"
