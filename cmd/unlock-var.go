package cmd

/*	License: GPLv3
	Authors:
		Mirko Brombin <mirko@fabricators.ltd>
		Vanilla OS Contributors <https://github.com/vanilla-os/>
	Copyright: 2024
	Description:
		ABRoot is utility which provides full immutability and
		atomicity to a Linux system, by transacting between
		two root filesystems. Updates are performed using OCI
		images, to ensure that the system is always in a
		consistent state.
*/

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/vanilla-os/abroot/core"
	"github.com/vanilla-os/abroot/settings"
	"github.com/vanilla-os/orchid/cmdr"
)

type VarInvalidError struct {
	passedDisk string
}

func (e *VarInvalidError) Error() string {
	return "the /var disk " + e.passedDisk + " does not exist"
}

type NotEncryptedError struct{}

func (e *NotEncryptedError) Error() string {
	return "the var partition is not encrypted"
}

func NewUnlockVarCommand() *cmdr.Command {
	cmd := cmdr.NewCommand(
		"unlock-var",
		"",
		"",
		unlockVarCmd,
	)

	cmd.WithBoolFlag(
		cmdr.NewBoolFlag(
			"dry-run",
			"d",
			"perform a dry run of the operation",
			false,
		),
	)

	// this is just meant for compatability with old Installations
	cmd.WithStringFlag(
		cmdr.NewStringFlag(
			"var-disk",
			"m",
			"pass /var disk directly instead of reading from configuration",
			"",
		),
	)

	cmd.WithBoolFlag(
		cmdr.NewBoolFlag(
			"check-encrypted",
			"c",
			"check if drive is encrypted and return",
			false,
		),
	)

	cmd.Example = "abroot unlock-var"

	cmd.Hidden = true

	return cmd
}

// helper function which only returns syntax errors and prints other ones
func unlockVarCmd(cmd *cobra.Command, args []string) error {
	err := unlockVar(cmd, args)
	if err != nil {
		cmdr.Error.Println(err)
		os.Exit(1)
		return nil
	}
	return nil
}

func unlockVar(cmd *cobra.Command, _ []string) error {
	if !core.RootCheck(false) {
		cmdr.Error.Println("You must be root to run this command.")
		os.Exit(2)
		return nil
	}

	varDisk, err := cmd.Flags().GetString("var-disk")
	if err != nil {
		return err
	}

	check_only, err := cmd.Flags().GetBool("check-encrypted")
	if err != nil {
		return err
	}

	_, err = os.Stat(filepath.Join("/dev/disk/by-label/", settings.Cnf.PartLabelVar))
	if err == nil || !errors.Is(err, os.ErrNotExist) {
		return &NotEncryptedError{}
	}
	if check_only {
		cmdr.Info.Println("The var partition is encrypted.")
		return nil
	}

	if varDisk == "" {
		if _, err := os.Lstat("/dev/mapper/vos--var-var"); err == nil {
			varDisk = "/dev/mapper/vos--var-var"
		} else if path, err := filepath.EvalSymlinks("/dev/disk/by-partlabel/vos-var"); err == nil {
			varDisk = path
		} else if settings.Cnf.PartCryptVar != "" {
			varDisk = settings.Cnf.PartCryptVar
		} else {
			return &VarInvalidError{}
		}
	}

	dryRun, err := cmd.Flags().GetBool("dry-run")
	if err != nil {
		return err
	}

	partitions, err := core.NewDiskManager().GetPartitions("")
	if err != nil {
		return err
	}

	var varLuksPart core.Partition
	foundPart := false

	for _, partition := range partitions {
		devName := "/dev/"
		if partition.IsDevMapper() {
			devName += "mapper/"
		}
		devName += partition.Device

		if devName == varDisk {
			varLuksPart = partition
			foundPart = true
			break
		}
	}
	if !foundPart {
		return &VarInvalidError{varDisk}
	}

	uuid := varLuksPart.Uuid
	cmdr.FgDefault.Println("unlocking", varDisk)

	if dryRun {
		cmdr.Info.Println("Dry run complete.")
	} else {
		// Try graphical unlock if available
		if canUsePlymouth() {
			err := unlockWithPlymouth(varDisk, uuid)
			if err == nil {
				cmdr.Info.Println("The system mounts have been performed successfully.")
				return nil
			}
			cmdr.Warning.Println("Graphical unlock failed, falling back to console:", err)
		}

		// Fallback to console unlock
		cryptsetupCmd := exec.Command("/usr/sbin/cryptsetup", "luksOpen", varDisk, "luks-"+uuid)
		cryptsetupCmd.Stdin = os.Stdin
		cryptsetupCmd.Stderr = os.Stderr
		cryptsetupCmd.Stdout = os.Stdout
		err := cryptsetupCmd.Run()
		if err != nil {
			return err
		}
		cmdr.Info.Println("The system mounts have been performed successfully.")
	}

	return nil
}

func canUsePlymouth() bool {
	cmd := exec.Command("plymouth", "--ping")
	return cmd.Run() == nil
}

func unlockWithPlymouth(device, uuid string) error {
	plymouthCmd := exec.Command("plymouth", "ask-for-password", "--prompt=Please enter passphrase to unlock your data.")
	plymouthCmd.Stderr = os.Stderr

	out, err := plymouthCmd.Output()
	if err != nil {
		return err
	}

	password := strings.TrimSpace(string(out))
	if password == "" {
		return errors.New("empty password entered")
	}

	cryptsetupCmd := exec.Command("/usr/sbin/cryptsetup", "luksOpen", device, "luks-"+uuid)
	stdinPipe, err := cryptsetupCmd.StdinPipe()
	if err != nil {
		return err
	}
	defer stdinPipe.Close()

	if err := cryptsetupCmd.Start(); err != nil {
		return err
	}

	_, err = io.WriteString(stdinPipe, password+"\n")
	if err != nil {
		return err
	}

	return cryptsetupCmd.Wait()
}
