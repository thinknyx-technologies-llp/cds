package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path"
	"strings"

	"github.com/kardianos/osext"

	"github.com/ovh/cds/sdk"
	"github.com/ovh/cds/sdk/log"
)

type script struct {
	shell   string
	content []byte
	opts    []string
}

func prepareScriptContent(parameters *[]sdk.Parameter) (*script, error) {
	var script = script{
		shell: "/bin/sh",
	}

	// Get script content
	var scriptContent string
	a := sdk.ParameterFind(parameters, "script")
	scriptContent = a.Value

	// Check that script content is there
	if scriptContent == "" {
		return nil, errors.New("script content not provided, aborting")
	}

	// except on windows where it's powershell
	if sdk.GOOS == "windows" {
		script.shell = "PowerShell"
		script.opts = []string{"-ExecutionPolicy", "Bypass", "-Command"}
		// on windows, we add ErrorActionPreference just below
	} else if strings.HasPrefix(scriptContent, "#!") { // If user wants a specific shell, use it
		t := strings.SplitN(scriptContent, "\n", 2)
		script.shell = strings.TrimPrefix(t[0], "#!")             // Find out the shebang
		script.shell = strings.TrimRight(script.shell, " \t\r\n") // Remove all the trailing shit
		splittedShell := strings.Split(script.shell, " ")         // Split it to find options
		script.shell = splittedShell[0]
		script.opts = splittedShell[1:]
		// if it's a shell, we add set -e to failed job when a command is failed
		if isShell(script.shell) && len(splittedShell) == 1 {
			script.opts = append(script.opts, "-e")
		}
		scriptContent = t[1]
	} else {
		script.opts = []string{"-e"}
	}

	script.content = []byte(scriptContent)

	return &script, nil
}

func writeScriptContent(script *script, basedir string) (func(), error) {
	// Create a tmp file
	tmpscript, errt := ioutil.TempFile(basedir, "cds-")
	if errt != nil {
		log.Warning("Cannot create tmp file: %s", errt)
		return nil, errors.New("cannot create temporary file, aborting")
	}

	// Put script in file
	n, errw := tmpscript.Write(script.content)
	if errw != nil || n != len(script.content) {
		if errw != nil {
			log.Warning("cannot write script: %s", errw)
		} else {
			log.Warning("cannot write all script: %d/%d", n, len(script.content))
		}
		return nil, errors.New("cannot write script in temporary file, aborting")
	}

	oldPath := tmpscript.Name()
	tmpscript.Close()
	var scriptPath string
	if sdk.GOOS == "windows" {
		//Remove all .txt Extensions, there is not always a .txt extension
		newPath := strings.Replace(oldPath, ".txt", "", -1)
		//and add .PS1 extension
		newPath = newPath + ".PS1"
		if err := os.Rename(oldPath, newPath); err != nil {
			return nil, errors.New("cannot rename script to add powershell Extension, aborting")
		}
		//This aims to stop a the very first error and return the right exit code
		psCommand := fmt.Sprintf("& { $ErrorActionPreference='Stop'; & %s ;exit $LastExitCode}", newPath)
		scriptPath = newPath
		script.opts = append(script.opts, psCommand)
	} else {
		scriptPath = oldPath
		script.opts = append(script.opts, scriptPath)
	}
	deferFunc := func() { os.Remove(scriptPath) }

	// Chmod file
	if err := os.Chmod(scriptPath, 0755); err != nil {
		log.Warning("runScriptAction> cannot chmod script %s: %s", scriptPath, err)
		return deferFunc, errors.New("cannot chmod script, aborting")
	}

	return deferFunc, nil
}

func runScriptAction(w *currentWorker) BuiltInAction {
	return func(ctx context.Context, a *sdk.Action, buildID int64, params *[]sdk.Parameter, secrets []sdk.Variable, sendLog LoggerFunc) sdk.Result {
		chanRes := make(chan sdk.Result)

		go func() {
			res := sdk.Result{Status: sdk.StatusSuccess.String()}
			script, err := prepareScriptContent(&a.Parameters)
			if err != nil {
				res.Status = sdk.StatusFail.String()
				res.Reason = err.Error()
				sendLog(res.Reason)
				chanRes <- res
			}

			deferFunc, err := writeScriptContent(script, w.basedir)
			if deferFunc != nil {
				defer deferFunc()
			}
			if err != nil {
				res.Status = sdk.StatusFail.String()
				res.Reason = err.Error()
				sendLog(res.Reason)
				chanRes <- res
			}

			log.Info("runScriptAction> %s %s", script.shell, strings.Trim(fmt.Sprint(script.opts), "[]"))
			cmd := exec.CommandContext(ctx, script.shell, script.opts...)
			res.Status = sdk.StatusUnknown.String()

			env := os.Environ()
			cmd.Env = []string{"CI=1"}
			// filter technical env variables
			for _, e := range env {
				if strings.HasPrefix(e, "CDS_") {
					continue
				}
				cmd.Env = append(cmd.Env, e)
			}

			//We have to let it here for some legacy reason
			cmd.Env = append(cmd.Env, "CDS_KEY=********")

			// worker export http port
			cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%d", WorkerServerPort, w.exportPort))

			//DEPRECATED - BEGIN
			// manage keys
			if w.currentJob.pkey != "" && w.currentJob.gitsshPath != "" {
				cmd.Env = append(cmd.Env, fmt.Sprintf("PKEY=%s", w.currentJob.pkey))
				cmd.Env = append(cmd.Env, fmt.Sprintf("GIT_SSH=%s", w.currentJob.gitsshPath))
			}
			//DEPRECATED - END

			//set up environment variables from pipeline build job parameters
			for _, p := range *params {
				// avoid put private key in environment var as it's a binary value
				if strings.HasPrefix(p.Name, "cds.key.") && strings.HasSuffix(p.Name, ".priv") {
					continue
				}
				if p.Type == sdk.KeyParameter && !strings.HasSuffix(p.Name, ".pub") {
					continue
				}

				cmd.Env = append(cmd.Env, cdsEnvVartoENV(p)...)

				envName := strings.Replace(p.Name, ".", "_", -1)
				envName = strings.Replace(envName, "-", "_", -1)
				envName = strings.ToUpper(envName)
				cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", envName, p.Value))
			}

			for _, p := range w.currentJob.buildVariables {
				envName := strings.Replace(p.Name, ".", "_", -1)
				envName = strings.Replace(envName, "-", "_", -1)
				envName = strings.ToUpper(envName)
				cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", envName, p.Value))
			}

			workerpath, err := osext.Executable()
			if err != nil {
				log.Warning("runScriptAction: Cannot get worker path: %s", err)
				res.Reason = "Failure due to internal error (Worker Path)"
				sendLog(res.Reason)
				res.Status = sdk.StatusFail.String()
				chanRes <- res
			}

			log.Info("Worker binary path: %s", path.Dir(workerpath))
			for i := range cmd.Env {
				if strings.HasPrefix(cmd.Env[i], "PATH") {
					cmd.Env[i] = fmt.Sprintf("%s:%s", cmd.Env[i], path.Dir(workerpath))
					break
				}
			}

			stdout, err := cmd.StdoutPipe()
			if err != nil {
				log.Warning("runScriptAction: Cannot get stdout pipe: %s", err)
				res.Reason = "Failure due to internal error"
				sendLog(res.Reason)
				res.Status = sdk.StatusFail.String()
				chanRes <- res
			}

			stderr, err := cmd.StderrPipe()
			if err != nil {
				log.Warning("runScriptAction: Cannot get stderr pipe: %s", err)
				res.Reason = "Failure due to internal error"
				sendLog(res.Reason)
				res.Status = sdk.StatusFail.String()
				chanRes <- res
			}

			stdoutreader := bufio.NewReader(stdout)
			stderrreader := bufio.NewReader(stderr)

			outchan := make(chan bool)
			go func() {
				for {
					line, errs := stdoutreader.ReadString('\n')
					if errs != nil {
						stdout.Close()
						close(outchan)
						return
					}
					sendLog(line)
				}
			}()

			errchan := make(chan bool)
			go func() {
				for {
					line, errs := stderrreader.ReadString('\n')
					if errs != nil {
						stderr.Close()
						close(errchan)
						return
					}
					sendLog(line)
				}
			}()

			if err := cmd.Start(); err != nil {
				res.Reason = fmt.Sprintf("%s\n", err)
				sendLog(res.Reason)
				res.Status = sdk.StatusFail.String()
				chanRes <- res
			}

			<-outchan
			<-errchan
			if err := cmd.Wait(); err != nil {
				res.Reason = fmt.Sprintf("%s\n", err)
				sendLog(res.Reason)
				res.Status = sdk.StatusFail.String()
				chanRes <- res
			}

			res.Status = sdk.StatusSuccess.String()
			chanRes <- res
		}()

		defer w.drainLogsAndCloseLogger(ctx)

		var res sdk.Result
		// Wait for a result
		select {
		case <-ctx.Done():
			log.Error("CDS Worker execution canceled: %v", ctx.Err())
			sendLog("CDS Worker execution canceled")
			res = sdk.Result{
				Status: sdk.StatusFail.String(),
				Reason: "CDS Worker execution canceled",
			}
			break

		case res = <-chanRes:
			break
		}

		log.Info("runScriptAction> %s %s", res.GetStatus(), res.GetReason())
		return res
	}
}

func isShell(in string) bool {
	for _, v := range []string{"ksh", "bash", "sh", "zsh"} {
		if strings.HasSuffix(in, v) {
			return true
		}
	}
	return false
}
