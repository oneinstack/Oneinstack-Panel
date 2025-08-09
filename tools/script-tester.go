package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// TestResult 测试结果
type TestResult struct {
	TestName string
	Status   string
	Message  string
	Duration time.Duration
}

// ScriptTester 脚本测试器
type ScriptTester struct {
	scriptName string
	scriptType string
	dryRun     bool
	verbose    bool
	testOS     []string
}

func main() {
	var (
		scriptName = flag.String("script", "", "脚本名称 (必需)")
		scriptType = flag.String("type", "install", "脚本类型 (install/uninstall)")
		dryRun     = flag.Bool("dry-run", false, "仅检查语法，不执行")
		verbose    = flag.Bool("v", false, "详细输出")
		testOS     = flag.String("os", "", "指定测试的操作系统 (用逗号分隔)")
	)
	flag.Parse()

	if *scriptName == "" {
		fmt.Println("错误: 必须指定脚本名称")
		flag.Usage()
		os.Exit(1)
	}

	var osList []string
	if *testOS != "" {
		osList = strings.Split(*testOS, ",")
	} else {
		osList = []string{"ubuntu:20.04", "centos:8", "debian:11"}
	}

	tester := &ScriptTester{
		scriptName: *scriptName,
		scriptType: *scriptType,
		dryRun:     *dryRun,
		verbose:    *verbose,
		testOS:     osList,
	}

	if err := tester.RunTests(); err != nil {
		fmt.Printf("测试失败: %v\n", err)
		os.Exit(1)
	}
}

func (st *ScriptTester) RunTests() error {
	scriptPath := filepath.Join("scripts", st.scriptType, st.scriptName+".sh")

	if _, err := os.Stat(scriptPath); os.IsNotExist(err) {
		return fmt.Errorf("脚本文件不存在: %s", scriptPath)
	}

	fmt.Printf("🧪 开始测试脚本: %s\n", st.scriptName)
	fmt.Printf("📁 脚本路径: %s\n", scriptPath)
	fmt.Printf("🔧 测试模式: %s\n", map[bool]string{true: "dry-run", false: "full"}[st.dryRun])
	fmt.Println(strings.Repeat("=", 60))

	var results []TestResult

	// 1. 语法检查
	result := st.testSyntax(scriptPath)
	results = append(results, result)
	st.printResult(result)

	if result.Status == "FAIL" {
		st.printSummary(results)
		return fmt.Errorf("语法检查失败")
	}

	// 2. shellcheck 检查
	result = st.testShellcheck(scriptPath)
	results = append(results, result)
	st.printResult(result)

	// 3. 参数检查
	result = st.testParameters(scriptPath)
	results = append(results, result)
	st.printResult(result)

	// 4. 帮助信息检查
	result = st.testHelp(scriptPath)
	results = append(results, result)
	st.printResult(result)

	if !st.dryRun {
		// 5. Docker 环境测试
		for _, osImage := range st.testOS {
			result = st.testInDocker(scriptPath, osImage)
			results = append(results, result)
			st.printResult(result)
		}
	}

	st.printSummary(results)
	return nil
}

func (st *ScriptTester) testSyntax(scriptPath string) TestResult {
	start := time.Now()

	cmd := exec.Command("bash", "-n", scriptPath)
	output, err := cmd.CombinedOutput()

	result := TestResult{
		TestName: "语法检查",
		Duration: time.Since(start),
	}

	if err != nil {
		result.Status = "FAIL"
		result.Message = string(output)
	} else {
		result.Status = "PASS"
		result.Message = "语法正确"
	}

	return result
}

func (st *ScriptTester) testShellcheck(scriptPath string) TestResult {
	start := time.Now()

	// 检查 shellcheck 是否安装
	if _, err := exec.LookPath("shellcheck"); err != nil {
		return TestResult{
			TestName: "Shellcheck检查",
			Status:   "SKIP",
			Message:  "shellcheck 未安装",
			Duration: time.Since(start),
		}
	}

	cmd := exec.Command("shellcheck", "-x", scriptPath)
	output, err := cmd.CombinedOutput()

	result := TestResult{
		TestName: "Shellcheck检查",
		Duration: time.Since(start),
	}

	if err != nil {
		result.Status = "WARN"
		result.Message = string(output)
	} else {
		result.Status = "PASS"
		result.Message = "代码质量良好"
	}

	return result
}

func (st *ScriptTester) testParameters(scriptPath string) TestResult {
	start := time.Now()

	// 读取脚本内容检查参数支持
	content, err := os.ReadFile(scriptPath)
	if err != nil {
		return TestResult{
			TestName: "参数检查",
			Status:   "FAIL",
			Message:  "无法读取脚本文件",
			Duration: time.Since(start),
		}
	}

	scriptContent := string(content)

	// 检查必需的参数处理
	requiredPatterns := []string{
		"--help",
		"--version",
		"show_help",
		"parse_args",
	}

	var missing []string
	for _, pattern := range requiredPatterns {
		if !strings.Contains(scriptContent, pattern) {
			missing = append(missing, pattern)
		}
	}

	result := TestResult{
		TestName: "参数检查",
		Duration: time.Since(start),
	}

	if len(missing) > 0 {
		result.Status = "WARN"
		result.Message = fmt.Sprintf("缺少推荐的参数处理: %v", missing)
	} else {
		result.Status = "PASS"
		result.Message = "参数处理完整"
	}

	return result
}

func (st *ScriptTester) testHelp(scriptPath string) TestResult {
	start := time.Now()

	cmd := exec.Command("bash", scriptPath, "--help")
	output, err := cmd.CombinedOutput()

	result := TestResult{
		TestName: "帮助信息",
		Duration: time.Since(start),
	}

	// 检查是否正常退出且有输出
	if err == nil && len(output) > 0 {
		result.Status = "PASS"
		result.Message = "帮助信息正常"
		if st.verbose {
			result.Message += "\n" + string(output)
		}
	} else {
		result.Status = "WARN"
		result.Message = "帮助信息可能有问题"
		if len(output) > 0 {
			result.Message += ": " + string(output)
		}
	}

	return result
}

func (st *ScriptTester) testInDocker(scriptPath, osImage string) TestResult {
	start := time.Now()

	// 检查 Docker 是否可用
	if _, err := exec.LookPath("docker"); err != nil {
		return TestResult{
			TestName: fmt.Sprintf("Docker测试 (%s)", osImage),
			Status:   "SKIP",
			Message:  "Docker 未安装",
			Duration: time.Since(start),
		}
	}

	// 创建测试命令
	dockerCmd := []string{
		"docker", "run", "--rm",
		"-v", fmt.Sprintf("%s:/workspace", filepath.Dir(scriptPath)),
		osImage,
		"bash", "-c",
		fmt.Sprintf("cd /workspace && bash %s --help", filepath.Base(scriptPath)),
	}

	cmd := exec.Command(dockerCmd[0], dockerCmd[1:]...)
	output, err := cmd.CombinedOutput()

	result := TestResult{
		TestName: fmt.Sprintf("Docker测试 (%s)", osImage),
		Duration: time.Since(start),
	}

	if err != nil {
		result.Status = "FAIL"
		result.Message = fmt.Sprintf("Docker测试失败: %s", string(output))
	} else {
		result.Status = "PASS"
		result.Message = fmt.Sprintf("在 %s 中运行正常", osImage)
		if st.verbose {
			result.Message += "\n" + string(output)
		}
	}

	return result
}

func (st *ScriptTester) printResult(result TestResult) {
	var statusIcon string
	var statusColor string

	switch result.Status {
	case "PASS":
		statusIcon = "✅"
		statusColor = "\033[32m" // 绿色
	case "FAIL":
		statusIcon = "❌"
		statusColor = "\033[31m" // 红色
	case "WARN":
		statusIcon = "⚠️"
		statusColor = "\033[33m" // 黄色
	case "SKIP":
		statusIcon = "⏭️"
		statusColor = "\033[36m" // 青色
	default:
		statusIcon = "❓"
		statusColor = "\033[37m" // 白色
	}

	fmt.Printf("%s %s%-15s%s [%s%s%s] (%.2fs)\n",
		statusIcon,
		statusColor,
		result.TestName,
		"\033[0m", // 重置颜色
		statusColor,
		result.Status,
		"\033[0m", // 重置颜色
		result.Duration.Seconds(),
	)

	if result.Message != "" && (result.Status == "FAIL" || result.Status == "WARN" || st.verbose) {
		// 缩进消息内容
		lines := strings.Split(result.Message, "\n")
		for _, line := range lines {
			if line != "" {
				fmt.Printf("    %s\n", line)
			}
		}
	}
}

func (st *ScriptTester) printSummary(results []TestResult) {
	fmt.Println(strings.Repeat("=", 60))

	var pass, fail, warn, skip int
	var totalDuration time.Duration

	for _, result := range results {
		switch result.Status {
		case "PASS":
			pass++
		case "FAIL":
			fail++
		case "WARN":
			warn++
		case "SKIP":
			skip++
		}
		totalDuration += result.Duration
	}

	fmt.Printf("📊 测试总结:\n")
	fmt.Printf("   总计: %d | ✅ 通过: %d | ❌ 失败: %d | ⚠️  警告: %d | ⏭️  跳过: %d\n",
		len(results), pass, fail, warn, skip)
	fmt.Printf("   总耗时: %.2fs\n", totalDuration.Seconds())

	if fail > 0 {
		fmt.Printf("\n❌ 测试失败！请修复上述问题后重新测试。\n")
	} else if warn > 0 {
		fmt.Printf("\n⚠️  测试通过，但有警告。建议修复警告项以提高脚本质量。\n")
	} else {
		fmt.Printf("\n🎉 所有测试通过！脚本质量良好。\n")
	}

	// 提供改进建议
	if fail > 0 || warn > 0 {
		fmt.Printf("\n💡 改进建议:\n")

		for _, result := range results {
			if result.Status == "FAIL" || result.Status == "WARN" {
				fmt.Printf("   • %s: %s\n", result.TestName, st.getSuggestion(result.TestName))
			}
		}
	}
}

func (st *ScriptTester) getSuggestion(testName string) string {
	suggestions := map[string]string{
		"语法检查":         "检查脚本语法错误，确保所有语句正确",
		"Shellcheck检查": "运行 shellcheck 工具修复代码质量问题",
		"参数检查":         "添加 --help, --version 等标准参数支持",
		"帮助信息":         "实现 show_help 函数并在 --help 参数时调用",
	}

	if suggestion, ok := suggestions[testName]; ok {
		return suggestion
	}

	if strings.Contains(testName, "Docker测试") {
		return "确保脚本在目标操作系统中能正常运行"
	}

	return "请查看详细错误信息进行修复"
}

// 交互式测试模式
func (st *ScriptTester) interactiveTest() {
	reader := bufio.NewReader(os.Stdin)

	fmt.Printf("🔧 交互式测试模式\n")
	fmt.Printf("脚本: %s (%s)\n", st.scriptName, st.scriptType)
	fmt.Println(strings.Repeat("-", 40))

	for {
		fmt.Printf("\n选择测试项:\n")
		fmt.Printf("1. 语法检查\n")
		fmt.Printf("2. Shellcheck 检查\n")
		fmt.Printf("3. 参数测试\n")
		fmt.Printf("4. 帮助信息测试\n")
		fmt.Printf("5. Docker 测试\n")
		fmt.Printf("6. 全部测试\n")
		fmt.Printf("0. 退出\n")
		fmt.Printf("请选择 (0-6): ")

		input, _ := reader.ReadString('\n')
		choice := strings.TrimSpace(input)

		switch choice {
		case "0":
			fmt.Println("退出测试")
			return
		case "1":
			result := st.testSyntax(filepath.Join("scripts", st.scriptType, st.scriptName+".sh"))
			st.printResult(result)
		case "2":
			result := st.testShellcheck(filepath.Join("scripts", st.scriptType, st.scriptName+".sh"))
			st.printResult(result)
		case "3":
			result := st.testParameters(filepath.Join("scripts", st.scriptType, st.scriptName+".sh"))
			st.printResult(result)
		case "4":
			result := st.testHelp(filepath.Join("scripts", st.scriptType, st.scriptName+".sh"))
			st.printResult(result)
		case "5":
			fmt.Printf("选择操作系统 (ubuntu:20.04/centos:8/debian:11): ")
			osInput, _ := reader.ReadString('\n')
			osImage := strings.TrimSpace(osInput)
			if osImage == "" {
				osImage = "ubuntu:20.04"
			}
			result := st.testInDocker(filepath.Join("scripts", st.scriptType, st.scriptName+".sh"), osImage)
			st.printResult(result)
		case "6":
			st.RunTests()
		default:
			fmt.Printf("无效选择: %s\n", choice)
		}
	}
}
