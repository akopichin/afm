package memory

import (
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Load reads a store from a YAML file. Returns empty Store if file doesn't exist.
func Load(path string) (Store, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Store{}, nil
		}
		return Store{}, err
	}

	var s Store
	if err := yaml.Unmarshal(data, &s); err != nil {
		return Store{}, err
	}

	return s, nil
}

// Save writes a store to a YAML file atomically using temp+rename.
func Save(path string, s Store) error {
	data, err := yaml.Marshal(s)
	if err != nil {
		return err
	}

	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".mem-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}

	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}

	return os.Rename(tmpName, path)
}
