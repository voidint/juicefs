// Copyright (C) 2018-present Juicedata Inc.

package object

import (
	"bytes"
	"errors"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/juicedata/juicesync/utils"
)

const (
	dirSuffix = "/"
)

type filestore struct {
	defaultObjectStorage
	root       string
	lastListed string
	listing    chan *Object
	listerr    error
}

func (d *filestore) String() string {
	return "file://" + d.root
}

func (d *filestore) path(key string) string {
	return filepath.Join(d.root, key)
}

func (d *filestore) Get(key string, off, limit int64) (io.ReadCloser, error) {
	p := d.path(key)
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	if off > 0 {
		if _, err := f.Seek(off, 0); err != nil {
			f.Close()
			return nil, err
		}
	}
	if limit > 0 {
		defer f.Close()
		buf := make([]byte, limit)
		if n, err := f.Read(buf); err != nil {
			return nil, err
		} else {
			return ioutil.NopCloser(bytes.NewBuffer(buf[:n])), nil
		}
	}
	return f, err
}

func (d *filestore) Put(key string, in io.Reader) error {
	p := d.path(key)

	if strings.HasSuffix(key, dirSuffix) {
		return os.MkdirAll(p, os.FileMode(0700))
	}

	if err := os.MkdirAll(filepath.Dir(p), os.FileMode(0700)); err != nil {
		return err
	}
	f, err := os.Create(p)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, in)
	return err
}

func (d *filestore) Copy(dst, src string) error {
	r, err := d.Get(src, 0, -1)
	if err != nil {
		return err
	}
	return d.Put(dst, r)
}

func (d *filestore) Exists(key string) error {
	if utils.Exists(d.path(key)) {
		return nil
	}
	return errors.New("not exists")
}

func (d *filestore) Delete(key string) error {
	if d.Exists(key) != nil {
		return errors.New("not exists")
	}
	return os.Remove(d.path(key))
}

func isSymlinkAndDir(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}

// walk recursively descends path, calling w.
func walk(path string, info os.FileInfo, walkFn filepath.WalkFunc) error {
	err := walkFn(path, info, nil)
	if err != nil {
		if info.IsDir() && err == filepath.SkipDir {
			return nil
		}
		return err
	}

	if !info.IsDir() {
		return nil
	}

	names, err := readDirNames(path)
	if err != nil {
		return walkFn(path, info, err)
	}

	for _, name := range names {
		filename := filepath.Join(path, name)
		fileInfo, err := os.Stat(filename)
		if err != nil {
			if err := walkFn(filename, fileInfo, err); err != nil && err != filepath.SkipDir {
				return err
			}
		} else {
			err = walk(filename, fileInfo, walkFn)
			if err != nil {
				if !fileInfo.IsDir() || err != filepath.SkipDir {
					return err
				}
			}
		}
	}
	return nil
}

// Walk walks the file tree rooted at root, calling walkFn for each file or
// directory in the tree, including root. All errors that arise visiting files
// and directories are filtered by walkFn. The files are walked in lexical
// order, which makes the output deterministic but means that for very
// large directories Walk can be inefficient.
// Walk always follow symbolic links.
func Walk(root string, walkFn filepath.WalkFunc) error {
	info, err := os.Stat(root)
	if err != nil {
		err = walkFn(root, nil, err)
	} else {
		err = walk(root, info, walkFn)
	}
	if err == filepath.SkipDir {
		return nil
	}
	return err
}

// readDirNames reads the directory named by dirname and returns
// a sorted list of directory entries.
func readDirNames(dirname string) ([]string, error) {
	f, err := os.Open(dirname)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Readdir(-1)
	if err != nil {
		return nil, err
	}
	names := make([]string, len(fi))
	for i := range fi {
		if fi[i].IsDir() || isSymlinkAndDir(filepath.Join(dirname, fi[i].Name())) {
			names[i] = fi[i].Name() + dirSuffix
		} else {
			names[i] = fi[i].Name()
		}
	}
	sort.Strings(names)
	return names, nil
}

func (d *filestore) List(prefix, marker string, limit int64) ([]*Object, error) {
	if marker != d.lastListed || d.listing == nil {
		listed := make(chan *Object, 10240)
		go func() {
			d.listerr = Walk(d.root, func(path string, info os.FileInfo, err error) error {
				if err != nil {
					return err
				}
				key := path[len(d.root):]
				if key >= marker && strings.HasPrefix(key, prefix) && !info.IsDir() {
					listed <- &Object{key, info.Size(), info.ModTime()}
				}
				return nil
			})
			close(listed)
		}()
		d.listing = listed
	}
	var objs []*Object
	for len(objs) < int(limit) {
		obj := <-d.listing
		if obj == nil {
			break
		}
		if obj.Key >= marker {
			objs = append(objs, obj)
		}
	}
	if len(objs) == 0 {
		d.listing = nil
		err := d.listerr
		d.listerr = nil
		return nil, err
	}
	d.lastListed = objs[len(objs)-1].Key
	return objs, nil
}

func (d *filestore) Chtimes(path string, mtime time.Time) error {
	return os.Chtimes(filepath.Join(d.root, path), mtime, mtime)
}

func newDisk(root, accesskey, secretkey string) ObjectStorage {
	os.MkdirAll(root, 0755)
	return &filestore{root: root}
}

func init() {
	register("file", newDisk)
}