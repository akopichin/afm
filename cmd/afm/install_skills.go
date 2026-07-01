package main

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/akopichin/afm/assets"
)

func newInstallSkillsCmd() *cobra.Command {
	var skillsDir string
	var force bool

	cmd := &cobra.Command{
		Use:   "install-skills",
		Short: "Установить AFM Claude-скиллы в ~/.claude/skills/",
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := resolveSkillsDir(skillsDir)
			if err != nil {
				return err
			}
			return installSkills(dir, force)
		},
	}
	cmd.Flags().StringVar(&skillsDir, "skills-dir", "", "путь назначения (по умолчанию: ~/.claude/skills)")
	cmd.Flags().BoolVar(&force, "force", false, "перезаписать существующие файлы")
	return cmd
}

func resolveSkillsDir(override string) (string, error) {
	if override != "" {
		return override, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("home dir: %w", err)
	}
	return filepath.Join(home, ".claude", "skills"), nil
}

func installSkills(dest string, force bool) error {
	fmt.Printf("Установка AFM-скиллов в %s/\n", dest)

	return fs.WalkDir(assets.SkillsFS, "claude/skills", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		// p = "claude/skills/afm/SKILL.md" → rel = "afm/SKILL.md"
		rel := strings.TrimPrefix(p, "claude/skills/")
		skillName := path.Dir(rel) // "afm"

		destPath := filepath.Join(dest, filepath.FromSlash(rel))

		if !force {
			if _, statErr := os.Stat(destPath); statErr == nil {
				fmt.Printf("  - %s (пропущен, уже существует)\n", skillName)
				return nil
			}
		}

		if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
			return fmt.Errorf("mkdir %s: %w", filepath.Dir(destPath), err)
		}

		data, err := assets.SkillsFS.ReadFile(p)
		if err != nil {
			return fmt.Errorf("читаю embedded скилл %s: %w", p, err)
		}
		if err := os.WriteFile(destPath, data, 0644); err != nil {
			return fmt.Errorf("запись %s: %w", destPath, err)
		}
		fmt.Printf("  + %s\n", skillName)
		return nil
	})
}
