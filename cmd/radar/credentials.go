package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"

	"github.com/lifei6671/github-radar/internal/auth"
	"github.com/lifei6671/github-radar/internal/consts"
)

func runCredentialCommand(command string, args []string, stdin *os.File, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(stderr)
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("该命令不接受位置参数")
	}
	if command == consts.CommandGenerateSessionSecret {
		_, err := fmt.Fprintln(stdout, auth.GenerateSessionSecret())
		return err
	}
	password, err := readAdminPassword(stdin, stderr)
	if err != nil {
		return err
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(stdout, hash)
	return err
}

func readAdminPassword(stdin *os.File, stderr io.Writer) (string, error) {
	fd := int(stdin.Fd())
	if term.IsTerminal(fd) {
		fmt.Fprint(stderr, "后台密码（输入时隐藏）：")
		password, err := term.ReadPassword(fd)
		fmt.Fprintln(stderr)
		if err != nil {
			return "", fmt.Errorf("读取后台密码: %w", err)
		}
		return string(password), nil
	}
	reader := bufio.NewReaderSize(stdin, consts.AdminPasswordMaxBytes+2)
	line, err := reader.ReadSlice('\n')
	if errors.Is(err, bufio.ErrBufferFull) {
		return "", fmt.Errorf("后台密码不能超过 %d 字节", consts.AdminPasswordMaxBytes)
	}
	if err != nil && (!errors.Is(err, io.EOF) || len(line) == 0) {
		return "", fmt.Errorf("读取后台密码: %w", err)
	}
	password, newline := strings.CutSuffix(string(line), "\n")
	if newline {
		password = strings.TrimSuffix(password, "\r")
	}
	return password, nil
}
