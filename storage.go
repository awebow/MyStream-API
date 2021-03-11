package main

import (
	"os/exec"
	"strings"
)

func (app *App) StoreFile(src string, dst string) error {
	replacer := strings.NewReplacer("${src}", src, "${dst}", dst)
	args := app.Config.StoreCommand[1:]
	for i, s := range args {
		args[i] = replacer.Replace(s)
	}

	return exec.Command(app.Config.StoreCommand[0], args...).Run()
}
