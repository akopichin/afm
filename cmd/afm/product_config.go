package main

import (
	"regexp"
	"strings"

	"github.com/akopichin/afm/contracts/config"
)

var releaseVersionPattern = regexp.MustCompile(`^v?[0-9]+\.[0-9]+\.[0-9]+(-(alpha|beta)\.[0-9]+)?$`)

func loadProductConfig(globalDir, projectDir string) (config.Config, error) {
	cfg, err := config.LoadFrom(globalDir, projectDir)
	if err == nil && cfg.Docker.Image == "" {
		cfg.Docker.Image = productDockerImage(version)
	}
	return cfg, err
}

func productDockerImage(buildVersion string) string {
	if releaseVersionPattern.MatchString(buildVersion) && strings.Contains(buildVersion, "-") {
		return "akopichin/afm:v" + strings.TrimPrefix(buildVersion, "v")
	}
	return "akopichin/afm:3"
}
