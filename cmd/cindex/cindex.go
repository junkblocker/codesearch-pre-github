// Copyright 2011 The Go Authors.  All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime/pprof"
	"sort"

	"github.com/junkblocker/codesearch/index"
)

const (
	DEFAULT_FOLLOW_SYMLINKS             = true
	DEFAULT_MAX_FILE_LENGTH             = 1 << 30
	DEFAULT_MAX_LINE_LENGTH             = 2000
	DEFAULT_MAX_TEXT_TRIGRAMS           = 30000
	DEFAULT_MAX_INVALID_UTF8_PERCENTAGE = 0.1
)

var usageMessage = `usage: cindex [options] [path...]

Options:

  -verbose     print extra information
  -list        list indexed paths and exit
  -reset       discard existing index
  -indexpath FILE
               use specified FILE as the index path. Overrides $CSEARCHINDEX.
  -cpuprofile FILE
               write CPU profile to FILE
  -logskip     print why a file was skipped from indexing
  -follow-symlinks
               follow symlinked files and directories also (Default: %v)
  -maxFileLen BYTES
               skip indexing a file if longer than this size in bytes (Default: %v)
  -maxlinelen BYTES
               skip indexing a file if it has a line longer than this size in bytes (Default: %v)
  -maxtrigrams COUNT
               skip indexing a file if it has more than this number of trigrams (Default: %v)
  -maxinvalidutf8ratio RATIO
               skip indexing a file if it has more than this ratio of invalid UTF-8 sequences (Default: %v)

cindex prepares the trigram index for use by csearch.  The index is the
file named by $CSEARCHINDEX, or else $HOME/.csearchindex.

The simplest invocation is

	cindex path...

which adds the file or directory tree named by each path to the index.
For example:

	cindex $HOME/src /usr/include

or, equivalently:

	cindex $HOME/src
	cindex /usr/include

If cindex is invoked with no paths, it reindexes the paths that have
already been added, in case the files have changed.  Thus, 'cindex' by
itself is a useful command to run in a nightly cron job.

By default cindex adds the named paths to the index but preserves
information about other paths that might already be indexed
(the ones printed by cindex -list).  The -reset flag causes cindex to
delete the existing index before indexing the new paths.
With no path arguments, cindex -reset removes the index.
`

func usage() {
	fmt.Fprintf(os.Stderr, usageMessage, DEFAULT_FOLLOW_SYMLINKS, DEFAULT_MAX_FILE_LENGTH, DEFAULT_MAX_LINE_LENGTH, DEFAULT_MAX_TEXT_TRIGRAMS, DEFAULT_MAX_INVALID_UTF8_PERCENTAGE)
	os.Exit(2)
}

var (
	listFlag           = flag.Bool("list", false, "list indexed paths and exit")
	resetFlag          = flag.Bool("reset", false, "discard existing index")
	verboseFlag        = flag.Bool("verbose", false, "print extra information")
	cpuProfile         = flag.String("cpuprofile", "", "write cpu profile to this file")
	indexPath          = flag.String("indexpath", "", "specifies index path")
	logSkipFlag        = flag.Bool("logskip", false, "print why a file was skipped from indexing")
	followSymlinksFlag = flag.Bool("follow-symlinks", DEFAULT_FOLLOW_SYMLINKS, "follow symlinked files and directories also")
	// Tuning variables for detecting text files.
	// A file is assumed not to be text files (and thus not indexed) if
	// 1) if it contains an invalid UTF-8 sequences
	// 2) if it is longer than maxFileLength bytes
	// 3) if it contains a line longer than maxLineLen bytes,
	// or
	// 4) if it contains more than maxTextTrigrams distinct trigrams.
	maxFileLen          = flag.Int64("maxfilelen", DEFAULT_MAX_FILE_LENGTH, "skip indexing a file if longer than this size in bytes")
	maxLineLen          = flag.Int("maxlinelen", DEFAULT_MAX_LINE_LENGTH, "skip indexing a file if it has a line longer than this size in bytes")
	maxTextTrigrams     = flag.Int("maxtrigrams", DEFAULT_MAX_TEXT_TRIGRAMS, "skip indexing a file if it has more than this number of trigrams")
	maxInvalidUTF8Ratio = flag.Float64("maxinvalidutf8ratio", DEFAULT_MAX_INVALID_UTF8_PERCENTAGE, "skip indexing a file if it has more than this ratio of invalid UTF-8 sequences")
)

func walk(arg string, symlinkFrom string, out chan string, logskip bool) {
	filepath.Walk(arg, func(path string, info os.FileInfo, err error) error {
		if basedir, elem := filepath.Split(path); elem != "" {
			// Skip various temporary or "hidden" files or directories.
			if info != nil && info.IsDir() {
				if elem == ".git" || elem == ".hg" || elem == ".bzr" || elem == ".svn" || elem == ".svk" || elem == "SCCS" || elem == "CVS" || elem == "_darcs" || elem == "_MTN" || elem[0] == '#' || elem[0] == '~' || elem[len(elem)-1] == '~' || elem == ".fseventsd" || elem == ".Trashes" || elem == ".Spotlight-V100" || elem == ".DocumentRevisions-V100" || elem == ".dropbox.cache" || elem == ".SyncArchive" {
					if logskip {
						if symlinkFrom != "" {
							log.Printf("%s: skipped. VCS or backup directory", symlinkFrom+path[len(arg):])
						} else {
							log.Printf("%s: skipped. VCS or backup directory", path)
						}
					}
					return filepath.SkipDir
				}
			} else {
				if elem[0] == '#' || elem[0] == '~' || elem[len(elem)-1] == '~' || elem == "tags" || elem == ".DS_Store" || elem == ".csearchindex" || elem == ".SyncID" || elem == ".SyncIgnore" {
					if logskip {
						if symlinkFrom != "" {
							log.Printf("%s: skipped. Backup or undesirable file", symlinkFrom+path[len(arg):])
						} else {
							log.Printf("%s: skipped. Backup or undesirable file", path)
						}
					}
					return nil
				}
				if info != nil && info.Mode()&os.ModeSymlink != 0 {
					if !*followSymlinksFlag {
						if logskip {
							log.Printf("%s: skipped. Symlink", path)
						}
						return nil
					}
					var symlinkAs string
					if basedir[len(basedir)-1] == os.PathSeparator {
						symlinkAs = basedir + elem
					} else {
						symlinkAs = basedir + string(os.PathSeparator) + elem
					}
					if symlinkFrom != "" {
						symlinkAs = symlinkFrom + symlinkAs[len(arg):]
					}
					if p, err := filepath.EvalSymlinks(symlinkAs); err != nil {
						if symlinkFrom != "" {
							log.Printf("%s: skipped. Symlink could not be resolved", symlinkFrom+path[len(arg):])
						} else {
							log.Printf("%s: skipped. Symlink could not be resolved", path)
						}
					} else {
						walk(p, symlinkAs, out, logskip)
					}
					return nil
				}
			}
		}
		if err != nil {
			if symlinkFrom != "" {
				log.Printf("%s: skipped. Error: %s", symlinkFrom+path[len(arg):], err)
			} else {
				log.Printf("%s: skipped. Error: %s", path, err)
			}
			return nil
		}
		if info != nil {
			if info.Mode()&os.ModeType == 0 {
				if symlinkFrom == "" {
					out <- path
				} else {
					out <- symlinkFrom + path[len(arg):]
				}
			} else if !info.IsDir() {
				if logskip {
					if symlinkFrom != "" {
						log.Printf("%s: skipped. Unsupported path type", symlinkFrom+path[len(arg):])
					} else {
						log.Printf("%s: skipped. Unsupported path type", path)
					}
				}
			}
		} else {
			if logskip {
				if symlinkFrom != "" {
					log.Printf("%s: skipped. Could not stat.", symlinkFrom+path[len(arg):])
				} else {
					log.Printf("%s: skipped. Could not stat.", path)
				}
			}
		}
		return nil
	})
}

func main() {
	flag.Usage = usage
	flag.Parse()
	args := flag.Args()

	if *indexPath != "" {
		if err := os.Setenv("CSEARCHINDEX", *indexPath); err != nil {
			log.Fatal(err)
		}
	}

	if *listFlag {
		master := index.File()
		if stat, err := os.Stat(master); err != nil || stat == nil {
			log.Fatal("Index " + master + " is not accessible")
		} else if stat.IsDir() || !stat.Mode().IsRegular() {
			log.Fatal("Index " + master + " must point to an index file")
		}
		ix := index.Open(master)
		for _, arg := range ix.Paths() {
			fmt.Printf("%s\n", arg)
		}
		return
	}

	if *cpuProfile != "" {
		f, err := os.Create(*cpuProfile)
		if err != nil {
			log.Fatal(err)
		}
		defer f.Close()
		pprof.StartCPUProfile(f)
		defer pprof.StopCPUProfile()
	}

	if *resetFlag && len(args) == 0 {
		master := index.File()
		stat, err := os.Stat(master)
		if err != nil {
			// does not exist so nothing to do
			return
		}
		if stat != nil && !stat.IsDir() && stat.Mode().IsRegular() {
			os.Remove(master)
			return
		} else {
			log.Fatal("Invalid index path " + master)
		}
	}
	if len(args) == 0 {
		ix := index.Open(index.File())
		for _, arg := range ix.Paths() {
			args = append(args, arg)
		}
		ix.Close()
	}

	// Translate paths to absolute paths so that we can
	// generate the file list in sorted order.
	for i, arg := range args {
		a, err := filepath.Abs(arg)
		if err != nil {
			log.Printf("%s: %s", arg, err)
			args[i] = ""
			continue
		}
		args[i] = a
	}
	sort.Strings(args)

	for len(args) > 0 && args[0] == "" {
		args = args[1:]
	}

	master := index.File()
	if stat, err := os.Stat(master); err != nil {
		// Does not exist.
		*resetFlag = true
	} else {
		if stat != nil && (stat.IsDir() || !stat.Mode().IsRegular()) {
			log.Fatal("Invalid index path " + master)
		}

	}
	file := master
	if !*resetFlag {
		file += "~"
	}

	ix := index.Create(file)
	ix.Verbose = *verboseFlag
	ix.LogSkip = *logSkipFlag
	ix.MaxFileLen = *maxFileLen
	ix.MaxLineLen = *maxLineLen
	ix.MaxTextTrigrams = *maxTextTrigrams
	ix.MaxInvalidUTF8Ratio = *maxInvalidUTF8Ratio
	ix.AddPaths(args)

	walkChan := make(chan string)
	doneChan := make(chan bool)

	go func() {
		seen := make(map[string]bool)
		for {
			select {
			case path := <-walkChan:
				if !seen[path] {
					seen[path] = true
					ix.AddFile(path)
				}
			case <-doneChan:
				return
			}
		}
	}()
	for _, arg := range args {
		log.Printf("index %s", arg)
		walk(arg, "", walkChan, *logSkipFlag)
	}
	doneChan <- true
	log.Printf("flush index")
	ix.Flush()

	if !*resetFlag {
		log.Printf("merge %s %s", master, file)
		index.Merge(file+"~", master, file)
		os.Remove(file)
		os.Remove(master)
		if err := os.Rename(file+"~", master); err != nil {
			log.Fatalf("failed to merge indexes: %s", err)
		}
	}
	log.Printf("done")
	return
}
