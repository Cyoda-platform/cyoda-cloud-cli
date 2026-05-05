package version

import "fmt"

var Version = "dev"

func UserAgent(v, os, arch string) string {
	return fmt.Sprintf("cyoda-cloud-cli/%s (%s %s)", v, os, arch)
}
