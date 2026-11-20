package installer

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

type configurationFile struct {
	path  string
	data  []byte
	mode  os.FileMode
	group int
}

type configurationBackup struct {
	path  string
	mode  os.FileMode
	user  int
	group int
}

func replaceConfiguration(files []configurationFile, activate, restoreService func() error) (err error) {
	backups := make([]configurationBackup, len(files))
	for index, file := range files {
		backup, err := backupIfExists(file.path)
		if err != nil {
			return fmt.Errorf("back up %s: %w", file.path, err)
		}
		backups[index] = backup
	}

	written := 0
	activationAttempted := false
	defer func() {
		if err == nil {
			return
		}
		for index := written - 1; index >= 0; index-- {
			path := files[index].path
			if restoreErr := backups[index].restore(path); restoreErr != nil {
				err = errors.Join(err, fmt.Errorf("restore %s: %w", path, restoreErr))
			}
		}
		if activationAttempted {
			err = errors.Join(err, restoreService())
		}
	}()

	for _, file := range files {
		if err := writeFileAtomic(file.path, file.data, file.mode, -1, file.group); err != nil {
			return fmt.Errorf("write %s: %w", file.path, err)
		}
		written++
	}
	activationAttempted = true
	return activate()
}

func writeFileAtomic(path string, data []byte, mode os.FileMode, uid, gid int) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()

	if _, err := tmp.Write(data); err != nil {
		return err
	}
	if uid != -1 || gid != -1 {
		if err := tmp.Chown(uid, gid); err != nil {
			return err
		}
	}
	if err := tmp.Chmod(mode); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func backupIfExists(path string) (configurationBackup, error) {
	original, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return configurationBackup{}, nil
	}
	if err != nil {
		return configurationBackup{}, err
	}
	defer original.Close()

	info, err := original.Stat()
	if err != nil {
		return configurationBackup{}, err
	}
	if !info.Mode().IsRegular() {
		return configurationBackup{}, errors.New("expected a regular file")
	}
	name := filepath.Base(path) + ".bak." + time.Now().UTC().Format("20060102T150405Z") + ".*"
	file, err := os.CreateTemp(filepath.Dir(path), name)
	if err != nil {
		return configurationBackup{}, err
	}
	_, copyErr := io.Copy(file, original)
	if err := errors.Join(copyErr, file.Close()); err != nil {
		os.Remove(file.Name())
		return configurationBackup{}, err
	}

	owner := info.Sys().(*syscall.Stat_t)
	return configurationBackup{path: file.Name(), mode: info.Mode().Perm(), user: int(owner.Uid), group: int(owner.Gid)}, nil
}

func (backup configurationBackup) restore(path string) error {
	if backup.path == "" {
		return os.Remove(path)
	}
	data, err := os.ReadFile(backup.path)
	if err != nil {
		return err
	}
	return writeFileAtomic(path, data, backup.mode, backup.user, backup.group)
}
