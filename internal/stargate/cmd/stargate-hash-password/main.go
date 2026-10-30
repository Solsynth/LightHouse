// stargate-hash-password generates a bcrypt hash for [auth.singleUser]
// passwordHash. The password is read from stdin so it never lands in shell
// history.
package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

func main() {
	fmt.Fprintln(os.Stderr, "Password:")
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		fmt.Fprintln(os.Stderr, "read password:", err)
		os.Exit(1)
	}
	password := strings.TrimRight(line, "\r\n")
	if password == "" {
		fmt.Fprintln(os.Stderr, "password is empty")
		os.Exit(1)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		fmt.Fprintln(os.Stderr, "hash password:", err)
		os.Exit(1)
	}
	fmt.Println(string(hash))
}
