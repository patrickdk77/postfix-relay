// pftool replaces the pfdel, pfhold, pfunhold and find_hold scripts for a
// single Postfix instance. Symlink it under each of those names, or run
// "pftool <name> ...".
package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

var commands = map[string]func([]string) error{
	"pfdel":     cmdDel,
	"pfhold":    cmdHold,
	"pfunhold":  cmdUnhold,
	"find_hold": cmdFindHold,
}

var errUsage = errors.New("usage")

func usage() {
	fmt.Fprint(os.Stderr, `usage:
  pfdel <address|domain>            delete queued mail from or to the address
  pfhold <address|domain>           put queued mail from or to the address on hold
  pfunhold <address|domain> [max]   release held mail from or to the address, at most max messages
  find_hold                         process mail_sender_policy HOLD decisions (see find_hold.conf)
`)
}

func main() {
	name := filepath.Base(os.Args[0])
	args := os.Args[1:]
	if _, ok := commands[name]; !ok {
		if len(args) == 0 {
			usage()
			os.Exit(2)
		}
		name, args = args[0], args[1:]
	}
	run, ok := commands[name]
	if !ok {
		usage()
		os.Exit(2)
	}
	if err := run(args); err != nil {
		if errors.Is(err, errUsage) {
			usage()
			os.Exit(2)
		}
		fmt.Fprintf(os.Stderr, "%s: %v\n", name, err)
		os.Exit(1)
	}
}

func requireRoot() error {
	if os.Geteuid() != 0 {
		return errors.New("you must be root to change queue files")
	}
	return nil
}

func cmdDel(args []string) error {
	if len(args) != 1 {
		return errUsage
	}
	if err := requireRoot(); err != nil {
		return err
	}
	entries, err := listQueue()
	if err != nil {
		return err
	}
	ids := selectDelete(entries, args[0])
	fmt.Printf("pfdel: %d message(s) match %s\n", len(ids), args[0])
	return postsuper("-d", ids)
}

func cmdHold(args []string) error {
	if len(args) != 1 {
		return errUsage
	}
	if err := requireRoot(); err != nil {
		return err
	}
	entries, err := listQueue()
	if err != nil {
		return err
	}
	ids := selectHold(entries, args[0])
	fmt.Printf("pfhold: %d message(s) match %s\n", len(ids), args[0])
	return postsuper("-h", ids)
}

func cmdUnhold(args []string) error {
	if len(args) < 1 || len(args) > 2 {
		return errUsage
	}
	max := -1
	if len(args) == 2 {
		n, err := strconv.Atoi(args[1])
		if err != nil || n < 0 {
			return fmt.Errorf("max must be a non-negative integer, got %q", args[1])
		}
		max = n
	}
	if err := requireRoot(); err != nil {
		return err
	}
	entries, err := listQueue()
	if err != nil {
		return err
	}
	ids := selectRelease(entries, args[0], max)
	fmt.Printf("pfunhold: %d held message(s) match %s\n", len(ids), args[0])
	return postsuper("-H", ids)
}
