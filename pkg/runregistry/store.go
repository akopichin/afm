package runregistry

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/akopichin/afm/contracts/fsutil"
)

const recordSuffix = ".json"

func recordPath(dir, instanceID string) string {
	return filepath.Join(dir, instanceID+recordSuffix)
}

// Publish writes the record of its process, creating or replacing the file atomically. A record is only ever
// rewritten by the process that owns it, so concurrent writers do not exist.
func Publish(dir string, rec Record) error {
	if !ValidInstanceID(rec.InstanceID) {
		return fmt.Errorf("runregistry: invalid instance id %q", rec.InstanceID)
	}
	rec.SchemaVersion = SchemaVersion
	if n := len(rec.PreviousRunIDs); n > MaxPreviousRunIDs {
		rec.PreviousRunIDs = rec.PreviousRunIDs[n-MaxPreviousRunIDs:]
	}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return fmt.Errorf("runregistry: encode record: %w", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("runregistry: create %s: %w", dir, err)
	}
	return fsutil.AtomicWriteFile(recordPath(dir, rec.InstanceID), data, 0o600)
}

// Remove deletes the record of one instance; a missing record is not an error.
func Remove(dir, instanceID string) error {
	if !ValidInstanceID(instanceID) {
		return fmt.Errorf("runregistry: invalid instance id %q", instanceID)
	}
	if err := os.Remove(recordPath(dir, instanceID)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// List returns the readable records. Temp files, broken or foreign-version files and files whose name does not
// match their instance id are skipped: the directory is shared with processes of other afm versions.
func List(dir string) ([]Record, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var records []Record
	for _, entry := range entries {
		id, ok := strings.CutSuffix(entry.Name(), recordSuffix)
		if !ok || !entry.Type().IsRegular() || !ValidInstanceID(id) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			continue
		}
		var rec Record
		if json.Unmarshal(data, &rec) != nil || rec.SchemaVersion != SchemaVersion || rec.InstanceID != id {
			continue
		}
		records = append(records, rec)
	}
	return records, nil
}
