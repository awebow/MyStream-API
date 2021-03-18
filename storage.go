package main

import (
	"os/exec"
	"strings"
)

func (app *App) StoreFile(src string, dst string) error {
	replacer := strings.NewReplacer("${src}", src, "${dst}", dst)
	args := make([]string, len(app.Config.StoreCommand)-1)
	for i := range args {
		args[i] = replacer.Replace(app.Config.StoreCommand[i+1])
	}

	return exec.Command(app.Config.StoreCommand[0], args...).Run()
}
