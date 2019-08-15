package experiments

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"time"

	"wel/formulas"
	"wel/services/host"
	"wel/webdriver"

	"github.com/logrusorgru/aurora"
	"github.com/sclevine/agouti"
)

/*
ExperimentConfig holds information needed while running any Experiment
*/
type ExperimentConfig struct {
	BrowserstackUser   string
	BrowserstackAPIKey string
	FrontEndDistPath   string // A file path to the build front-end resources
	PublicPageHostURL  string // A public URL (usually provided by ngrok) for the page formula host
	PageHostPort       int64
	BaselineMode       bool // True if the target embed script should be served as a no-op script
}

func GatherExperimentBaseline(
	experiment *Experiment,
	experimentConfig *ExperimentConfig,
) ([]*BaselineData, error) {

	baselineData := []*BaselineData{}

	// This is the logic run against every browser/page formula combination in the experiment
	testingFunc := func(page *agouti.Page, hasBrowserLog bool, testProbes []string, probeBasis formulas.ProbeBasis) error {
		testsJSON, err := json.Marshal(testProbes)
		if err != nil {
			return err
		}

		var returnValue string
		script := fmt.Sprintf(`
					try {
						let results = await runWebEmbedLabBaseline(%s);
						callback(JSON.stringify(results));
					} catch (e) {
						console.error('Error running baseline: ' + e);
						let results = {
							'wel-failure': { passed: false, error: 'error running baseline' }
						}
						callback(JSON.stringify(results));
					}
					`, testsJSON)
		page.RunAsyncScript(script, &returnValue)
		logger.Println("Baseline script returned:", returnValue)

		// TODO append results to baselineData

		return nil
	}

	err := executeExperiment(experiment, experimentConfig, nil, testingFunc)
	if err != nil {
		logger.Println("Failed to gather baseline", err)
		return nil, err
	}

	return baselineData, nil
}

/*
RunExperiment follows this algorithm to run an experiment:
	For each test run defined in the experiment:
		For each browser in the test run:
			Open a WebDriver connection
			For each page formula:
				Load a blank page to stop previous pages' loads
				Tell the host to host the page formula
				Tell the browser to open the correct host URL
				Run the specified test probes and collect results
*/
func RunExperimentTests(
	experiment *Experiment,
	experimentConfig *ExperimentConfig,
	baselineData []*BaselineData,
) (string, bool) {
	gatheredResults := []*ProbeResults{}
	gatheredReturnValues := []string{}

	// This is the logic run against every browser/page formula combination in the experiment
	testingFunc := func(page *agouti.Page, hasBrowserLog bool, testProbes []string, probeBasis formulas.ProbeBasis) error {
		testsJSON, err := json.Marshal(testProbes)
		if err != nil {
			return err
		}
		probeBasisJSON, err := json.Marshal(probeBasis)
		if err != nil {
			return err
		}
		if probeBasisJSON == nil || string(probeBasisJSON) == "null" {
			probeBasisJSON = []byte("{}")
		}

		var returnValue string
		script := fmt.Sprintf(`
					try {
						let results = await runWebEmbedLabProbes(
							%s,
							%s
						);
						callback(JSON.stringify(results));
					} catch (e) {
						console.error('Error running probes: ' + e);
						let results = {
							'wel-failure': { passed: false, error: 'error running the tests' }
						}
						callback(JSON.stringify(results));
					}
					`, testsJSON, string(probeBasisJSON))
		page.RunAsyncScript(script, &returnValue)

		probeResults := &ProbeResults{}
		err = json.Unmarshal([]byte(returnValue), probeResults)
		if err != nil {
			return err
		}
		hasAFail := false
		for testName, result := range *probeResults {
			if result.Passed() {
				logger.Println(testName+":", aurora.Green("passed"))
			} else {
				hasAFail = true
				logger.Println(testName+":", aurora.Red("failed"))
				if basis, ok := probeBasis[testName]; ok == true {
					marshalledBasis, err := json.MarshalIndent(basis, "", "\t")
					if err != nil {
						logger.Println(aurora.Red("Expected:"), basis)
					} else {
						logger.Println(aurora.Red("Expected:"), string(marshalledBasis))
					}
				}
				marshalledResult, err := json.MarshalIndent(result, "", "\t")
				if err != nil {
					logger.Println(aurora.Red("Received:"), result)
				} else {
					logger.Println(aurora.Red("Received:"), string(marshalledResult))
				}
			}
		}
		gatheredResults = append(gatheredResults, probeResults)
		gatheredReturnValues = append(gatheredReturnValues, returnValue)

		if hasAFail {
			if hasBrowserLog {
				if logs, err := page.ReadNewLogs("browser"); err != nil {
					logger.Println("Error fetching logs", err)
				} else {
					for _, log := range logs {
						logger.Println("Log:", log.Message)
					}
				}
			} else {
				logger.Println("Browser does not provide logs :-(")
			}
		}
		return nil
	}
	err := executeExperiment(experiment, experimentConfig, baselineData, testingFunc)
	if err != nil {
		logger.Println("Failed to run tests", err)
		return "", false
	}

	hasFailure := false
	for _, probeResults := range gatheredResults {
		if probeResults.Passed() == false {
			hasFailure = true
		}
	}
	returnJSON, err := json.MarshalIndent(gatheredResults, "", "\t")
	if err != nil {
		logger.Println("Error serializing gathered results", err)
		return "", hasFailure == false
	}
	return string(returnJSON), hasFailure == false

}

func executeExperiment(
	experiment *Experiment,
	experimentConfig *ExperimentConfig,
	baselineData []*BaselineData,
	testingFunc func(*agouti.Page, bool, []string, formulas.ProbeBasis) error,
) error {
	for index, testRun := range experiment.TestRuns {
		if baselineData == nil {
			logger.Println(aurora.Bold("Baseline Run #"), aurora.Bold(index))
		} else {
			logger.Println(aurora.Bold("Testing Run #"), aurora.Bold(index))
		}

		// Opening the browser is the slowest part of a test run so open each browser only once
		for _, browserName := range testRun.Browsers {
			// Make sure we have a browser configuration
			browserConfig, ok := experiment.GetBrowserConfiguration(browserName)
			if ok == false {
				logger.Println("Unknown browser configuration:", browserName)
				return errors.New("Unknown browser configuration")
			}

			logger.Println("Connecting to browser:", browserName)
			page, hasBrowserLog, err := openPage(experimentConfig, browserConfig)
			if err != nil {
				logger.Println("Failed to open remote page:", err)
				return err
			}
			defer page.Destroy() // Close the WebDriver session

			/*
				Navigate to a blank page.
				This is necessary to let the prober-extension get its hooks into the page so that we can get sample early in loading the page formulas.
			*/
			err = page.Navigate(experimentConfig.PublicPageHostURL + host.BlankURL)
			if err != nil {
				logger.Println("Failed to navigate to blank page", err)
				return err
			}

			for _, pageFormulaName := range testRun.PageFormulas {
				pageFormulaConfig, ok := experiment.GetPageFormulaConfiguration(pageFormulaName)
				if ok == false {
					logger.Println("Unknown page formula:", pageFormulaName)
					return err
				}

				// Host the right page formula and parse the test probe basis
				formulaSet, controlResponse, err := host.RequestPageFormulaChange(experimentConfig.PageHostPort, pageFormulaConfig.Name, baselineData == nil)
				if err != nil {
					logger.Println("Failed to reach host control API", err)
					return err
				}
				if formulaSet == false {
					logger.Println("Failed to host page formula", pageFormulaConfig.Name)
					return err
				}

				// Reset the browser
				err = page.Reset()
				if err != nil {
					logger.Println("Failed to reset page", err)
					return err
				}
				page.ReadNewLogs("browser")

				// Navigate the browser to the right URL
				logger.Printf("Navigating to %v...", pageFormulaConfig.Name)
				err = page.Navigate(experimentConfig.PublicPageHostURL + controlResponse.InitialPath)
				if err != nil {
					logger.Println("Failed to navigate to hosted page formula", err)
					return err
				}
				logger.Printf("Initial navigation complete.")

				time.Sleep(5 * time.Second)

				// Run the tests
				logger.Printf("Running '%v' on '%v':", pageFormulaConfig.Name, browserName)
				err = testingFunc(page, hasBrowserLog, testRun.TestProbes, controlResponse.ProbeBasis)
				if err != nil {
					logger.Println("Failed to run script", err)
					return err
				}
			}
			page.Destroy()
		}
	}
	return nil
}

/*
openPage opens a WebDriver connection to a browser
Returns (page, canProvideLogs, error)
*/
func openPage(experimentConfig *ExperimentConfig, browserConfiguration map[string]interface{}) (*agouti.Page, bool, error) {
	// On Chrome, load the prober-extension
	extensionPath := experimentConfig.FrontEndDistPath + "prober-extension/prober-extension.xpi"
	crxBytes, err := ioutil.ReadFile(extensionPath)
	if err != nil {
		logger.Println(aurora.Red(fmt.Sprintf("Error reading extension (%v): %v", extensionPath, err)))
		return nil, false, err
	}

	capabilities := agouti.NewCapabilities()
	capabilities["browserstack.user"] = experimentConfig.BrowserstackUser
	capabilities["browserstack.key"] = experimentConfig.BrowserstackAPIKey
	capabilities["browserstack.console"] = "verbose"
	capabilities["browserstack.seleniumLogs"] = "true"
	capabilities["chromeOptions"] = map[string][][]byte{
		"extensions": {crxBytes},
	}
	for key, value := range browserConfiguration {
		capabilities[key] = value
	}
	page, err := agouti.NewPage(webdriver.BrowserstackURL, []agouti.Option{agouti.Desired(capabilities)}...)
	if err != nil {
		return nil, false, err
	}

	hasBrowserLog := false
	logTypes, err := page.LogTypes()
	if err == nil {
		for _, logType := range logTypes {
			if logType == "browser" {
				hasBrowserLog = true
				break
			}
		}
	}
	return page, hasBrowserLog, nil
}
