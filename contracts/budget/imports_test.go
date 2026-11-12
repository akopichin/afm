package budget

import "os/exec"

func goListDeps() (string, error) {
	out, err := exec.Command("go", "list", "-deps", "github.com/akopichin/afm/contracts/budget").CombinedOutput()
	return string(out), err
}
