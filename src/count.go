package main

import (
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	var files int
	var lines int
	err := filepath.Walk(".", func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if filepath.Ext(path) != ".go" {
			return nil
		}
		fmt.Println(files, path)
		content, err := ioutil.ReadFile(path)
		if err != nil {
			return err
		}
		lines += strings.Count(string(content), "\n")
		files++

		return nil
	})
	if err != nil {
		fmt.Printf("Error: %s\n", err)
		return
	}
	fmt.Printf("Files: %d\n", files)
	fmt.Printf("Lines: %d\n", lines)
}
