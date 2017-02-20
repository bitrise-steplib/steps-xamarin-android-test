package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/bitrise-io/depman/pathutil"
	"github.com/bitrise-io/go-utils/command"
	"github.com/bitrise-io/go-utils/fileutil"
	"github.com/bitrise-io/go-utils/log"
	"github.com/bitrise-tools/go-xamarin/builder"
	"github.com/bitrise-tools/go-xamarin/constants"
	"github.com/bitrise-tools/go-xamarin/tools/nunit"
)

// ConfigsModel ...
type ConfigsModel struct {
	XamarinSolution      string
	XamarinConfiguration string
	XamarinPlatform      string

	TestToRun string

	EmulatorSerial string

	DeployDir string
}

func createConfigsModelFromEnvs() ConfigsModel {
	return ConfigsModel{
		XamarinSolution:      os.Getenv("xamarin_project"),
		XamarinConfiguration: os.Getenv("xamarin_configuration"),
		XamarinPlatform:      os.Getenv("xamarin_platform"),

		TestToRun:      os.Getenv("test_to_run"),
		EmulatorSerial: os.Getenv("emulator_serial"),

		DeployDir: os.Getenv("BITRISE_DEPLOY_DIR"),
	}
}

func (configs ConfigsModel) print() {
	log.Infof("Build Configs:")

	log.Printf("- XamarinSolution: %s", configs.XamarinSolution)
	log.Printf("- XamarinConfiguration: %s", configs.XamarinConfiguration)
	log.Printf("- XamarinPlatform: %s", configs.XamarinPlatform)

	log.Infof("Xamarin UITest Configs:")

	log.Printf("- TestToRun: %s", configs.TestToRun)
	log.Printf("- EmulatorSerial: %s", configs.EmulatorSerial)

	log.Infof("Other Configs:")

	log.Printf("- DeployDir: %s", configs.DeployDir)
}

func (configs ConfigsModel) validate() error {
	if configs.XamarinSolution == "" {
		return errors.New("no XamarinSolution parameter specified")
	}
	if exist, err := pathutil.IsPathExists(configs.XamarinSolution); err != nil {
		return fmt.Errorf("failed to check if XamarinSolution exist at: %s, error: %s", configs.XamarinSolution, err)
	} else if !exist {
		return fmt.Errorf("XamarinSolution not exist at: %s", configs.XamarinSolution)
	}

	if configs.XamarinConfiguration == "" {
		return errors.New("no XamarinConfiguration parameter specified")
	}
	if configs.XamarinPlatform == "" {
		return errors.New("no XamarinPlatform parameter specified")
	}

	if configs.EmulatorSerial == "" {
		return errors.New("no EmulatorSerial parameter specified")
	}

	return nil
}

func exportEnvironmentWithEnvman(keyStr, valueStr string) error {
	cmd := command.New("envman", "add", "--key", keyStr)
	cmd.SetStdin(strings.NewReader(valueStr))
	return cmd.Run()
}

func testResultLogContent(pth string) (string, error) {
	if exist, err := pathutil.IsPathExists(pth); err != nil {
		return "", fmt.Errorf("Failed to check if path (%s) exist, error: %s", pth, err)
	} else if !exist {
		return "", fmt.Errorf("test result not exist at: %s", pth)
	}

	content, err := fileutil.ReadStringFromFile(pth)
	if err != nil {
		return "", fmt.Errorf("Failed to read file (%s), error: %s", pth, err)
	}

	return content, nil
}

func parseErrorFromResultLog(content string) (string, error) {
	failureLineFound := false
	lastFailureMessage := ""

	scanner := bufio.NewScanner(strings.NewReader(content))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		if line == "<failure>" {
			failureLineFound = true
			continue
		}

		if failureLineFound && strings.HasPrefix(line, "<message>") {
			lastFailureMessage = line
		}

		failureLineFound = false
	}

	return lastFailureMessage, nil
}

func failf(format string, v ...interface{}) {
	log.Errorf(format, v...)

	if err := exportEnvironmentWithEnvman("BITRISE_XAMARIN_TEST_RESULT", "failed"); err != nil {
		log.Warnf("Failed to export environment: %s, error: %s", "BITRISE_XAMARIN_TEST_RESULT", err)
	}

	os.Exit(1)
}

func main() {
	configs := createConfigsModelFromEnvs()

	fmt.Println()
	configs.print()

	if err := configs.validate(); err != nil {
		failf("Issue with input: %s", err)
	}

	// Nunit Console path
	nunitConsolePth, err := nunit.SystemNunit3ConsolePath()
	if err != nil {
		failf("Failed to get system insatlled nunit3-console.exe path, error: %s", err)
	}
	// ---

	//
	// build
	fmt.Println()
	log.Infof("Building all Android Xamarin UITest and Referred Projects in solution: %s", configs.XamarinSolution)

	builder, err := builder.New(configs.XamarinSolution, []constants.SDK{constants.SDKAndroid}, false)
	if err != nil {
		failf("Failed to create xamarin builder, error: %s", err)
	}

	callback := func(solutionName string, projectName string, sdk constants.SDK, testFramework constants.TestFramework, commandStr string, alreadyPerformed bool) {
		fmt.Println()
		if testFramework == constants.TestFrameworkXamarinUITest {
			log.Infof("Building test project: %s", projectName)
		} else {
			log.Infof("Building project: %s", projectName)
		}

		log.Donef("$ %s", commandStr)

		if alreadyPerformed {
			log.Warnf("build command already performed, skipping...")
		}

		fmt.Println()
	}

	warnings, err := builder.BuildAllXamarinUITestAndReferredProjects(configs.XamarinConfiguration, configs.XamarinPlatform, nil, callback)
	for _, warning := range warnings {
		log.Warnf(warning)
	}
	if err != nil {
		failf("Build failed, error: %s", err)
	}

	projectOutputMap, err := builder.CollectProjectOutputs(configs.XamarinConfiguration, configs.XamarinPlatform)
	if err != nil {
		failf("Failed to collect project outputs, error: %s", err)
	}

	testProjectOutputMap, warnings, err := builder.CollectXamarinUITestProjectOutputs(configs.XamarinConfiguration, configs.XamarinPlatform)
	for _, warning := range warnings {
		log.Warnf(warning)
	}
	if err != nil {
		failf("Failed to collect test project output, error: %s", err)
	}
	// ---

	//
	// Run nunit tests
	nunitConsole, err := nunit.New(nunitConsolePth)
	if err != nil {
		failf("Failed to create nunit console model, error: %s", err)
	}

	resultLogPth := filepath.Join(configs.DeployDir, "TestResult.xml")
	nunitConsole.SetResultLogPth(resultLogPth)

	// Artifacts
	resultLog := ""

	for testProjectName, testProjectOutput := range testProjectOutputMap {
		if len(testProjectOutput.ReferredProjectNames) == 0 {
			log.Warnf("Test project (%s) does not refers to any project, skipping...", testProjectName)
			continue
		}

		for _, projectName := range testProjectOutput.ReferredProjectNames {
			projectOutput, ok := projectOutputMap[projectName]
			if !ok {
				continue
			}

			apkPth := ""
			for _, output := range projectOutput.Outputs {
				if output.OutputType == constants.OutputTypeAPK {
					apkPth = output.Pth
				}
			}

			if apkPth == "" {
				failf("No apk generated for project: %s", projectName)
			}

			// Set ANDROID_APK_PATH env to let the test know which .apk file should be tested
			// This env is used in the Xamarin.UITest project to refer to the .apk path
			if err := os.Setenv("ANDROID_APK_PATH", apkPth); err != nil {
				failf("Failed to set ANDROID_APK_PATH environment, without this env test will fail, error: %s", err)
			}

			// Run test
			fmt.Println()
			log.Infof("Testing (%s) against (%s)", testProjectName, projectName)
			log.Printf("test dll: %s", testProjectOutput.Output.Pth)
			log.Printf("apk: %s", apkPth)

			nunitConsole.SetDLLPth(testProjectOutput.Output.Pth)
			nunitConsole.SetTestToRun(configs.TestToRun)

			fmt.Println()
			log.Infof("Running Xamarin UITest")
			log.Donef("$ %s", nunitConsole.PrintableCommand())
			fmt.Println()

			err := nunitConsole.Run()
			testLog, readErr := testResultLogContent(resultLogPth)
			if readErr != nil {
				log.Warnf("Failed to read test result, error: %s", readErr)
			}
			resultLog = testLog

			if err != nil {
				if errorMsg, err := parseErrorFromResultLog(resultLog); err != nil {
					log.Warnf("Failed to parse error message from result log, error: %s", err)
				} else if errorMsg != "" {
					log.Errorf("%s", errorMsg)
				}

				if resultLog != "" {
					if err := exportEnvironmentWithEnvman("BITRISE_XAMARIN_TEST_FULL_RESULTS_TEXT", resultLog); err != nil {
						log.Warnf("Failed to export environment: %s, error: %s", "BITRISE_XAMARIN_TEST_FULL_RESULTS_TEXT", err)
					}
				}

				failf("Test failed, error: %s", err)
			}
		}
	}

	if err := exportEnvironmentWithEnvman("BITRISE_XAMARIN_TEST_RESULT", "succeeded"); err != nil {
		log.Warnf("Failed to export environment: %s, error: %s", "BITRISE_XAMARIN_TEST_RESULT", err)
	}

	if resultLog != "" {
		if err := exportEnvironmentWithEnvman("BITRISE_XAMARIN_TEST_FULL_RESULTS_TEXT", resultLog); err != nil {
			log.Warnf("Failed to export environment: %s, error: %s", "BITRISE_XAMARIN_TEST_FULL_RESULTS_TEXT", err)
		}
	}
}