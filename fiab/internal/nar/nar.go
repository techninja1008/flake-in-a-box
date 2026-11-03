// Package nar serialises filesystem trees in the Nix ARchive format.
package nar

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
)

type writer struct {
	w   io.Writer
	buf [8]byte
}

func (w *writer) str(s string) error {
	binary.LittleEndian.PutUint64(w.buf[:], uint64(len(s)))
	if _, err := w.w.Write(w.buf[:]); err != nil {
		return err
	}
	if _, err := io.WriteString(w.w, s); err != nil {
		return err
	}
	return w.pad(uint64(len(s)))
}

func (w *writer) pad(n uint64) error {
	if r := n % 8; r != 0 {
		var zero [8]byte
		_, err := w.w.Write(zero[:8-r])
		return err
	}
	return nil
}

func (w *writer) strs(ss ...string) error {
	for _, s := range ss {
		if err := w.str(s); err != nil {
			return err
		}
	}
	return nil
}

// Dump writes the NAR serialisation of path to out.
func Dump(path string, out io.Writer) error {
	w := &writer{w: out}
	if err := w.str("nix-archive-1"); err != nil {
		return err
	}
	return w.node(path)
}

func (w *writer) node(path string) error {
	fi, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if err := w.str("("); err != nil {
		return err
	}
	switch mode := fi.Mode(); {
	case mode.IsRegular():
		if err := w.strs("type", "regular"); err != nil {
			return err
		}
		if mode&0o100 != 0 {
			if err := w.strs("executable", ""); err != nil {
				return err
			}
		}
		if err := w.str("contents"); err != nil {
			return err
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		size := uint64(fi.Size())
		binary.LittleEndian.PutUint64(w.buf[:], size)
		if _, err := w.w.Write(w.buf[:]); err != nil {
			return err
		}
		n, err := io.Copy(w.w, f)
		if err != nil {
			return err
		}
		if uint64(n) != size {
			return fmt.Errorf("%s: size changed while reading", path)
		}
		if err := w.pad(size); err != nil {
			return err
		}
	case mode&os.ModeSymlink != 0:
		target, err := os.Readlink(path)
		if err != nil {
			return err
		}
		if err := w.strs("type", "symlink", "target", target); err != nil {
			return err
		}
	case mode.IsDir():
		if err := w.strs("type", "directory"); err != nil {
			return err
		}
		ents, err := os.ReadDir(path)
		if err != nil {
			return err
		}
		names := make([]string, len(ents))
		for i, e := range ents {
			names[i] = e.Name()
		}
		sort.Strings(names)
		for _, name := range names {
			if err := w.strs("entry", "(", "name", name, "node"); err != nil {
				return err
			}
			if err := w.node(filepath.Join(path, name)); err != nil {
				return err
			}
			if err := w.str(")"); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("%s: unsupported file type %v", path, mode)
	}
	return w.str(")")
}
