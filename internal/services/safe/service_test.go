package safe

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"oneinstack/internal/models"
)

type recordedCommand struct {
	name string
	args []string
}

type fakeRunner struct {
	mu        sync.Mutex
	paths     map[string]bool
	status    string
	commands  []recordedCommand
	failMatch string
	failCount int
	failures  map[string]int
	outputs   map[string]string
}

func (f *fakeRunner) LookPath(name string) (string, error) {
	if f.paths[name] {
		return "/usr/sbin/" + name, nil
	}
	return "", errors.New("not found")
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.commands = append(f.commands, recordedCommand{name: name, args: append([]string{}, args...)})
	joined := name + " " + strings.Join(args, " ")
	for match, remaining := range f.failures {
		if remaining > 0 && strings.Contains(joined, match) {
			f.failures[match] = remaining - 1
			return []byte("simulated failure"), errors.New("simulated failure")
		}
	}
	if f.failCount > 0 && strings.Contains(joined, f.failMatch) {
		f.failCount--
		return []byte("simulated failure"), errors.New("simulated failure")
	}
	for match, output := range f.outputs {
		if strings.Contains(joined, match) {
			return []byte(output), nil
		}
	}
	if name == "ufw" && len(args) == 1 && args[0] == "status" {
		if f.status == "" {
			return []byte("Status: active"), nil
		}
		return []byte(f.status), nil
	}
	if name == "firewall-cmd" && len(args) == 1 && args[0] == "--state" {
		return []byte("running"), nil
	}
	return nil, nil
}

func openFirewallTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:firewall-%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "-"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.IptablesRule{}); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestNormalizeRuleRejectsUnsafeInputAndPanelLockout(t *testing.T) {
	tests := []models.IptablesRule{
		{Direction: "in", Protocol: "tcp", Strategy: "allow", IPs: "1.2.3.4;touch /tmp/x", Ports: "80", State: 1},
		{Direction: "in", Protocol: "tcp", Strategy: "allow", IPs: "1.2.3.4", Ports: "70000", State: 1},
		{Direction: "in", Protocol: "icmp", Strategy: "allow", IPs: "", Ports: "80", State: 1},
		{Direction: "in", Protocol: "tcp", Strategy: "deny", IPs: "", Ports: "8000-9000", State: 1},
		{Direction: "in", Protocol: "tcp", Strategy: "deny", IPs: "", Ports: "", State: 1},
	}
	for index := range tests {
		if _, err := normalizeRule(&tests[index], 8089); !errors.Is(err, ErrValidation) {
			t.Fatalf("case %d: expected validation error, got %v", index, err)
		}
	}
}

func TestAddRollsBackSystemCommandsOnPartialFailure(t *testing.T) {
	db := openFirewallTestDB(t)
	runner := &fakeRunner{
		paths:  map[string]bool{"ufw": true},
		status: "Status: active", failMatch: "10.0.0.2", failCount: 1,
	}
	service := NewService(db, runner, 8089)
	rule := &models.IptablesRule{
		Direction: "in", Protocol: "tcp", Strategy: "allow",
		IPs: "10.0.0.1,10.0.0.2", Ports: "80", State: 1,
	}
	if err := service.Add(context.Background(), rule); err == nil {
		t.Fatal("expected add failure")
	}
	var count int64
	if err := db.Model(&models.IptablesRule{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("database changed after rollback: count=%d err=%v", count, err)
	}
	if !runner.hasCommand("ufw --force delete allow in from 10.0.0.1") {
		t.Fatalf("first system command was not rolled back: %#v", runner.commands)
	}
}

func TestUpdateFailureRestoresOldRuleAndDatabase(t *testing.T) {
	db := openFirewallTestDB(t)
	old := models.IptablesRule{
		Direction: "in", Protocol: "tcp", Strategy: "allow", IPs: "0.0.0.0/0",
		Ports: "80", State: 1, Backend: BackendUFW, Token: "stable-token",
	}
	if err := db.Create(&old).Error; err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{
		paths: map[string]bool{"ufw": true}, status: "Status: active",
		failMatch: "port 443", failCount: 1,
	}
	service := NewService(db, runner, 8089)
	requested := &models.IptablesRule{
		ID: old.ID, Direction: "in", Protocol: "tcp", Strategy: "allow",
		IPs: "", Ports: "443", State: 1,
	}
	if err := service.Update(context.Background(), requested); err == nil {
		t.Fatal("expected update failure")
	}
	var restored models.IptablesRule
	if err := db.First(&restored, old.ID).Error; err != nil {
		t.Fatal(err)
	}
	if restored.Ports != "80" {
		t.Fatalf("database was changed despite rollback: %#v", restored)
	}
	if runner.commandCount("ufw allow in from any to any port 80") < 1 {
		t.Fatalf("old system rule was not restored: %#v", runner.commands)
	}
}

func TestProtectedRuleCannotBeChangedOrDeleted(t *testing.T) {
	db := openFirewallTestDB(t)
	rule := models.IptablesRule{
		Direction: "in", Protocol: "tcp", Strategy: "allow", IPs: "0.0.0.0/0",
		Ports: "8089", State: 1, Backend: BackendUFW, Token: "panel", Protected: true,
	}
	if err := db.Create(&rule).Error; err != nil {
		t.Fatal(err)
	}
	service := NewService(db, &fakeRunner{paths: map[string]bool{"ufw": true}}, 8089)
	if err := service.Delete(context.Background(), rule.ID); !errors.Is(err, ErrProtected) {
		t.Fatalf("expected protected error, got %v", err)
	}
	requested := rule
	requested.Ports = "8090"
	if err := service.Update(context.Background(), &requested); !errors.Is(err, ErrProtected) {
		t.Fatalf("expected protected error, got %v", err)
	}
}

func TestEnableCreatesPanelRuleBeforeUFWEnable(t *testing.T) {
	db := openFirewallTestDB(t)
	runner := &fakeRunner{
		paths:  map[string]bool{"ufw": true},
		status: "Status: inactive",
	}
	service := NewService(db, runner, 8089)
	if err := service.SetEnabled(context.Background(), true, ""); err != nil {
		t.Fatal(err)
	}
	addIndex := runner.commandIndex("ufw allow in from any to any port 8089")
	enableIndex := runner.commandIndex("ufw --force enable")
	if addIndex < 0 || enableIndex < 0 || addIndex >= enableIndex {
		t.Fatalf("panel rule must be added before enabling firewall: %#v", runner.commands)
	}
	var protected models.IptablesRule
	if err := db.Where("protected = ?", true).First(&protected).Error; err != nil {
		t.Fatal(err)
	}
	if protected.Ports != "8089" {
		t.Fatalf("wrong protected port: %#v", protected)
	}
}

func TestEnableRepairsStalePanelProtectionMarker(t *testing.T) {
	db := openFirewallTestDB(t)
	stale := models.IptablesRule{
		Direction: "in", Protocol: "tcp", Strategy: "allow", IPs: "0.0.0.0/0",
		Ports: "8089", State: 1, Backend: BackendUFW, Token: "missing-token", Protected: true,
	}
	if err := db.Create(&stale).Error; err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{
		paths: map[string]bool{"ufw": true}, status: "Status: inactive",
		outputs: map[string]string{"ufw show added": "Added user rules:\n"},
	}
	service := NewService(db, runner, 8089)
	if err := service.SetEnabled(context.Background(), true, ""); err != nil {
		t.Fatal(err)
	}
	var rules []models.IptablesRule
	if err := db.Where("protected = ?", true).Find(&rules).Error; err != nil {
		t.Fatal(err)
	}
	if len(rules) != 1 || rules[0].Token == "missing-token" {
		t.Fatalf("stale marker was not replaced: %#v", rules)
	}
	if runner.commandIndex("ufw allow in from any to any port 8089") < 0 {
		t.Fatalf("system protection was not recreated: %#v", runner.commands)
	}
}

func TestDisableRequiresExplicitConfirmation(t *testing.T) {
	db := openFirewallTestDB(t)
	runner := &fakeRunner{paths: map[string]bool{"ufw": true}, status: "Status: active"}
	service := NewService(db, runner, 8089)
	if err := service.SetEnabled(context.Background(), false, "yes"); !errors.Is(err, ErrValidation) {
		t.Fatalf("expected validation error, got %v", err)
	}
	if runner.hasCommand("ufw --force disable") {
		t.Fatal("firewall was disabled without exact confirmation")
	}
}

func TestPreparedPanelPortCanBeRolledBack(t *testing.T) {
	db := openFirewallTestDB(t)
	runner := &fakeRunner{paths: map[string]bool{"ufw": true}, status: "Status: active"}
	service := NewService(db, runner, 8089)
	id, created, err := service.PreparePanelPort(context.Background(), 9443)
	if err != nil {
		t.Fatal(err)
	}
	if !created || id < 1 {
		t.Fatalf("expected a newly protected rule, id=%d created=%v", id, created)
	}
	if err := service.RollbackPreparedPanelPort(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := db.Model(&models.IptablesRule{}).Where("id = ?", id).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("prepared rule was not removed: count=%d err=%v", count, err)
	}
	if !runner.hasCommand("ufw --force delete allow in from any to any port 9443") {
		t.Fatalf("prepared system rule was not rolled back: %#v", runner.commands)
	}
}

func TestUFWPingEditRollsBackFileWhenReloadFails(t *testing.T) {
	db := openFirewallTestDB(t)
	runner := &fakeRunner{
		paths: map[string]bool{"ufw": true}, status: "Status: active",
		failMatch: "ufw reload", failCount: 1,
	}
	service := NewService(db, runner, 8089)
	path := filepath.Join(t.TempDir(), "before.rules")
	original := "*filter\n-A ufw-before-input -p icmp --icmp-type echo-request -j ACCEPT\nCOMMIT\n"
	if err := os.WriteFile(path, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	service.ufwBeforeRules = path
	if err := service.SetPingBlocked(context.Background(), true); err == nil {
		t.Fatal("expected reload failure")
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != original {
		t.Fatalf("UFW file was not rolled back:\n%s", content)
	}
	if runner.commandCount("ufw reload") != 2 {
		t.Fatalf("expected failed reload and rollback reload: %#v", runner.commands)
	}
}

func TestIPTablesRequiresPersistenceSupport(t *testing.T) {
	db := openFirewallTestDB(t)
	runner := &fakeRunner{paths: map[string]bool{"iptables": true}}
	service := NewService(db, runner, 8089)
	rule := &models.IptablesRule{
		Direction: "out", Protocol: "udp", Strategy: "allow",
		IPs: "8.8.8.8", Ports: "53", State: 1,
	}
	if err := service.Add(context.Background(), rule); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("expected persistence requirement, got %v", err)
	}
}

func TestFirewalldUsesRichRulesForInboundAndDirectRulesForOutbound(t *testing.T) {
	inbound := &models.IptablesRule{
		Direction: "in", Protocol: "tcp", Strategy: "allow",
		IPs: "192.0.2.0/24", Ports: "443", Backend: BackendFirewalld, Token: "inbound",
	}
	inboundOperations := firewalldRuleOperations(inbound, "firewall-cmd", true)
	if len(inboundOperations) != 1 {
		t.Fatalf("inbound operation count = %d", len(inboundOperations))
	}
	inboundCommand := strings.Join(inboundOperations[0].args, " ")
	if !strings.Contains(inboundCommand, "--add-rich-rule=") ||
		!strings.Contains(inboundCommand, `source address="192.0.2.0/24"`) {
		t.Fatalf("unexpected inbound command: %s", inboundCommand)
	}

	outbound := &models.IptablesRule{
		Direction: "out", Protocol: "udp", Strategy: "deny",
		IPs: "203.0.113.10", Ports: "53", Backend: BackendFirewalld, Token: "outbound",
	}
	outboundOperations := firewalldRuleOperations(outbound, "firewall-cmd", true)
	if len(outboundOperations) != 1 {
		t.Fatalf("outbound operation count = %d", len(outboundOperations))
	}
	outboundCommand := strings.Join(outboundOperations[0].args, " ")
	if !strings.Contains(outboundCommand, "--direct --add-rule ipv4 filter OUTPUT") ||
		!strings.Contains(outboundCommand, "-d 203.0.113.10 --dport 53") ||
		!strings.Contains(outboundCommand, "-j DROP") {
		t.Fatalf("unexpected outbound command: %s", outboundCommand)
	}
}

func TestFirewalldStatusReportsRepairAndMissingSystemd(t *testing.T) {
	db := openFirewallTestDB(t)
	runner := &fakeRunner{
		paths: map[string]bool{
			"firewall-cmd": true, "firewall-offline-cmd": true, "systemctl": true,
		},
		failures: map[string]int{
			"firewall-cmd --state":                1,
			"firewall-offline-cmd --check-config": 1,
			"systemctl show-environment":          1,
		},
	}
	status, err := NewService(db, runner, 8089).Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.Backend != BackendFirewalld || !status.Install || status.Enabled ||
		status.CanToggle || !status.RepairRequired {
		t.Fatalf("unexpected firewalld status: %#v", status)
	}
	if !strings.Contains(status.Warning, "配置校验失败") ||
		!strings.Contains(status.Warning, "systemd") {
		t.Fatalf("missing actionable warning: %q", status.Warning)
	}
}

func TestInactiveFirewalldPreferredOverInactiveUFW(t *testing.T) {
	db := openFirewallTestDB(t)
	runner := &fakeRunner{
		paths: map[string]bool{
			"ufw": true, "firewall-cmd": true,
			"firewall-offline-cmd": true, "systemctl": true,
		},
		status: "Status: inactive",
		failures: map[string]int{
			"firewall-cmd --state": 1,
		},
	}
	status, err := NewService(db, runner, 8089).Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.Backend != BackendFirewalld || !status.Install || status.Enabled {
		t.Fatalf("unexpected preferred backend: %#v", status)
	}
}

func TestFirewalldEnableRequiresValidConfiguration(t *testing.T) {
	db := openFirewallTestDB(t)
	runner := &fakeRunner{
		paths: map[string]bool{
			"firewall-cmd": true, "firewall-offline-cmd": true, "systemctl": true,
		},
		failures: map[string]int{
			"firewall-cmd --state":                1,
			"firewall-offline-cmd --check-config": 1,
		},
	}
	err := NewService(db, runner, 8089).SetEnabled(context.Background(), true, "")
	if !errors.Is(err, ErrUnsupported) || !strings.Contains(err.Error(), "修复") {
		t.Fatalf("expected repair requirement, got %v", err)
	}
	if runner.hasCommand("firewall-offline-cmd --add-rich-rule") ||
		runner.hasCommand("systemctl start firewalld") {
		t.Fatalf("invalid firewalld configuration was modified: %#v", runner.commands)
	}
}

func TestFirewalldEnableRequiresSystemd(t *testing.T) {
	db := openFirewallTestDB(t)
	runner := &fakeRunner{
		paths: map[string]bool{
			"firewall-cmd": true, "firewall-offline-cmd": true, "systemctl": true,
		},
		failures: map[string]int{
			"firewall-cmd --state":       1,
			"systemctl show-environment": 1,
		},
	}
	err := NewService(db, runner, 8089).SetEnabled(context.Background(), true, "")
	if !errors.Is(err, ErrUnsupported) || !strings.Contains(err.Error(), "systemd") {
		t.Fatalf("expected systemd requirement, got %v", err)
	}
	if runner.hasCommand("firewall-offline-cmd --add-rich-rule") {
		t.Fatalf("panel rule was written without a service manager: %#v", runner.commands)
	}
}

func TestFirewalldEnableProtectsPanelPortBeforeStarting(t *testing.T) {
	db := openFirewallTestDB(t)
	runner := &fakeRunner{
		paths: map[string]bool{
			"firewall-cmd": true, "firewall-offline-cmd": true, "systemctl": true,
		},
		failures: map[string]int{"firewall-cmd --state": 1},
	}
	if err := NewService(db, runner, 8089).SetEnabled(context.Background(), true, ""); err != nil {
		t.Fatal(err)
	}
	addIndex := runner.commandIndex("firewall-offline-cmd --add-rich-rule")
	startIndex := runner.commandIndex("systemctl start firewalld")
	if addIndex < 0 || startIndex < 0 || addIndex >= startIndex {
		t.Fatalf("panel rule must be written before firewalld starts: %#v", runner.commands)
	}
}

func (f *fakeRunner) commandIndex(fragment string) int {
	for index, command := range f.commands {
		if strings.Contains(command.name+" "+strings.Join(command.args, " "), fragment) {
			return index
		}
	}
	return -1
}

func (f *fakeRunner) commandCount(fragment string) int {
	count := 0
	for _, command := range f.commands {
		if strings.Contains(command.name+" "+strings.Join(command.args, " "), fragment) {
			count++
		}
	}
	return count
}

func (f *fakeRunner) hasCommand(fragment string) bool {
	return f.commandIndex(fragment) >= 0
}
