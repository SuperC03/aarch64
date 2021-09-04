package utils

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/fosshostorg/aarch64/daemons/internal/message"
	"go.uber.org/zap"
)

// Create the Bridge Network if Not Already Present
func CreateBridge(l *zap.Logger, data *message.VMData) error {
	// Check if Domain Interface Already Exists. If not, Create Bridge
	cmd := exec.Command("ip", "addr", "show")
	output, err := cmd.Output()
	if err != nil {
		l.Error(
			"unable to access system interfaces",
			zap.String("command", "ip addr show"),
			zap.Error(err),
		)
		return err
	}
	interfaceName := fmt.Sprintf("vbr%d", data.Index)
	if !strings.Contains(string(output), interfaceName) {
		// Delete Possibly Existing Bridge Network
		exec.Command("ip", "link", "del", interfaceName).Output()
		// Create New Bridge Network
		if _, err = exec.Command("ip", "link", "add", interfaceName, "type", "bridge").Output(); err != nil {
			l.Warn(
				"unable to create network virtual bridge",
				zap.String("command", "ip link add "+interfaceName+" type bridge"),
				zap.Error(err),
			)
			return err
		}
		// Give the New Bridge an IP Assignment
		if _, err = exec.Command("ip", "addr", "add", "dev", interfaceName, fmt.Sprintf("%s/64", data.Gateway)).Output(); err != nil {
			l.Error(
				"unable to assign ip address to new network bridge",
				zap.String("command", "ip addr add dev "+interfaceName+" "+data.Gateway+"/64"),
				zap.Error(err),
			)
			return err
		}
		// Start the Bridge Network
		if _, err = exec.Command("ip", "link", "set", "dev", interfaceName, "up").Output(); err != nil {
			l.Error(
				"unable to start network bridge",
				zap.String("command", "ip link set dev "+interfaceName+" up"),
				zap.Error(err),
			)
			return err
		}
	}
	return nil
}
