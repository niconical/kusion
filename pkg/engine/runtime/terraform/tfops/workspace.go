package tfops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	"github.com/hashicorp/hcl/v2/hclparse"
	"github.com/spf13/afero"

	"kusionstack.io/kusion/pkg/log"
	"kusionstack.io/kusion/pkg/models"
	"kusionstack.io/kusion/pkg/util/io"
	jsonutil "kusionstack.io/kusion/pkg/util/json"
	"kusionstack.io/kusion/pkg/util/kfile"
)

const (
	envLog            = "TF_LOG"
	envPluginCacheDir = "TF_PLUGIN_CACHE_DIR"
	tfDebugLOG        = "DEBUG"
	envLogPath        = "TF_LOG_PATH"
	LockHCLFile       = ".terraform.lock.hcl"
	mainTFFile        = "main.tf.json"
	tfPlanFile        = "plan.out"
	tfStateFile       = "terraform.tfstate"
	tfProviderPrefix  = "terraform-provider"
	terraformD        = ".terraform.d"
	pluginCache       = "plugin-cache"
)

var envTFLog = fmt.Sprintf("%s=%s", envLog, tfDebugLOG)

type WorkSpace struct {
	resource   *models.Resource
	fs         afero.Afero
	stackDir   string
	tfCacheDir string
}

// SetResource set workspace resource
func (w *WorkSpace) SetResource(resource *models.Resource) {
	w.resource = resource
}

// SetFS set filesystem
func (w *WorkSpace) SetFS(fs afero.Afero) {
	w.fs = fs
}

// SetStackDir set workspace work directory.
func (w *WorkSpace) SetStackDir(stackDir string) {
	w.stackDir = stackDir
}

// SetCacheDir set tf cache work directory.
func (w *WorkSpace) SetCacheDir(cacheDir string) {
	w.tfCacheDir = cacheDir
}

func NewWorkSpace(fs afero.Afero) *WorkSpace {
	return &WorkSpace{
		fs: fs,
	}
}

// WriteHCL convert kusion Resource to HCL json
// and write hcl json to main.tf.json
func (w *WorkSpace) WriteHCL() error {
	provider := strings.Split(w.resource.Extensions["provider"].(string), "/")
	resourceType := w.resource.Extensions["resourceType"].(string)
	resourceNames := strings.Split(w.resource.ResourceKey(), ":")
	if len(resourceNames) < 4 {
		return fmt.Errorf("illegial resource id:%s in Spec. "+
			"Resource id format: providerNamespace:providerName:resourceType:resourceName", w.resource.ResourceKey())
	}

	m := map[string]interface{}{
		"terraform": map[string]interface{}{
			"required_providers": map[string]interface{}{
				provider[len(provider)-2]: map[string]string{
					"source":  strings.Join(provider[:len(provider)-1], "/"),
					"version": provider[len(provider)-1],
				},
			},
		},
		"provider": map[string]interface{}{
			provider[len(provider)-2]: w.resource.Extensions["providerMeta"],
		},
		"resource": map[string]interface{}{
			resourceType: map[string]interface{}{
				resourceNames[len(resourceNames)-1]: w.resource.Attributes,
			},
		},
	}
	hclMain := jsonutil.Marshal2PrettyString(m)

	_, err := w.fs.Stat(w.tfCacheDir)
	if err != nil {
		if os.IsNotExist(err) {
			if err := w.fs.MkdirAll(w.tfCacheDir, os.ModePerm); err != nil {
				return fmt.Errorf("create workspace error: %v", err)
			}
		} else {
			return err
		}
	}
	err = w.fs.WriteFile(filepath.Join(w.tfCacheDir, mainTFFile), []byte(hclMain), 0o600)
	if err != nil {
		return fmt.Errorf("write hcl main.tf.json error: %v", err)
	}

	return nil
}

// WriteTFState writes StateRepresentation to the file, this function is for terraform apply refresh only
func (w *WorkSpace) WriteTFState(priorState *models.Resource) error {
	provider := strings.Split(priorState.Extensions["provider"].(string), "/")
	resourceNames := strings.Split(w.resource.ResourceKey(), ":")
	if len(resourceNames) < 4 {
		return fmt.Errorf("illegial resource id:%s in terraform.tfstate. "+
			"Resource id format: providerNamespace:providerName:resourceType:resourceName", w.resource.ResourceKey())
	}
	m := map[string]interface{}{
		"version": 4,
		"resources": []map[string]interface{}{
			{
				"mode":     "managed",
				"type":     priorState.Extensions["resourceType"].(string),
				"name":     resourceNames[len(resourceNames)-1],
				"provider": fmt.Sprintf("provider[\"%s\"]", strings.Join(provider[:len(provider)-1], "/")),
				"instances": []map[string]interface{}{
					{
						"attributes": priorState.Attributes,
					},
				},
			},
		},
	}
	hclState := jsonutil.Marshal2PrettyString(m)

	err := w.fs.WriteFile(filepath.Join(w.tfCacheDir, tfStateFile), []byte(hclState), os.ModePerm)
	if err != nil {
		return fmt.Errorf("write hcl error: %v", err)
	}
	return nil
}

// InitWorkSpace init terraform runtime workspace
func (w *WorkSpace) InitWorkSpace(ctx context.Context) error {
	chdir := fmt.Sprintf("-chdir=%s", w.tfCacheDir)
	cmd := exec.CommandContext(ctx, "terraform", chdir, "init")
	cmd.Dir = w.stackDir
	envs, err := w.initEnvs()
	if err != nil {
		return err
	}
	cmd.Env = envs
	_, err = cmd.Output()
	if e, ok := err.(*exec.ExitError); ok {
		return errors.New(string(e.Stderr))
	}
	return nil
}

func (w *WorkSpace) initEnvs() ([]string, error) {
	providerCachePath, err := getProviderCachePath()
	if err != nil {
		return nil, err
	}
	logPath, err := w.getEnvProviderLogPath()
	if err != nil {
		return nil, err
	}
	result := append(os.Environ(), envTFLog, providerCachePath, logPath)
	return result, nil
}

// Apply with the terraform cli apply command
func (w *WorkSpace) Apply(ctx context.Context) (*StateRepresentation, error) {
	chdir := fmt.Sprintf("-chdir=%s", w.tfCacheDir)
	err := w.CleanAndInitWorkspace(ctx)
	if err != nil {
		return nil, err
	}

	cmd := exec.CommandContext(ctx, "terraform", chdir, "apply", "-auto-approve", "-json", "-lock=false")
	cmd.Dir = w.stackDir
	envs, err := w.initEnvs()
	if err != nil {
		return nil, err
	}
	cmd.Env = envs

	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, TFError(out)
	}

	s, err := w.RefreshOnly(ctx)
	if err != nil {
		return nil, fmt.Errorf("terraform read state error: %v", err)
	}
	return s, err
}

// Plan with the terraform cli plan command
func (w *WorkSpace) Plan(ctx context.Context) (*PlanRepresentation, error) {
	chdir := fmt.Sprintf("-chdir=%s", w.tfCacheDir)
	err := w.CleanAndInitWorkspace(ctx)
	if err != nil {
		return nil, err
	}

	cmd := exec.CommandContext(ctx, "terraform", chdir, "plan", "-out="+tfPlanFile)
	cmd.Dir = w.stackDir
	envs, err := w.initEnvs()
	if err != nil {
		return nil, err
	}
	cmd.Env = envs

	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, TFError(out)
	}
	// convert plan result to PlanRepresentation
	pr, err := w.ShowPlan(ctx)
	if err != nil {
		return nil, fmt.Errorf("terraform show plan error: %v", err)
	}

	return pr, err
}

// ShowState shows local tfstate with the terraform cli show command
func (w *WorkSpace) ShowState(ctx context.Context) (*StateRepresentation, error) {
	fi, err := w.fs.Stat(filepath.Join(w.tfCacheDir, tfStateFile))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	out, err := w.show(ctx, fi.Name())
	if err != nil {
		return nil, err
	}

	s := &StateRepresentation{}
	if err = json.Unmarshal(out, s); err != nil {
		return nil, fmt.Errorf("json umarshal state failed: %v", err)
	}
	return s, nil
}

// ShowPlan shows local plan file with the terraform cli show command
func (w *WorkSpace) ShowPlan(ctx context.Context) (*PlanRepresentation, error) {
	fi, err := w.fs.Stat(filepath.Join(w.tfCacheDir, tfPlanFile))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	out, err := w.show(ctx, fi.Name())
	if err != nil {
		return nil, err
	}

	s := &PlanRepresentation{}
	if err = json.Unmarshal(out, s); err != nil {
		return nil, fmt.Errorf("json umarshal plan representation failed: %v", err)
	}
	return s, nil
}

func (w *WorkSpace) show(ctx context.Context, fileName string) ([]byte, error) {
	chdir := fmt.Sprintf("-chdir=%s", w.tfCacheDir)
	cmd := exec.CommandContext(ctx, "terraform", chdir, "show", "-json", fileName)
	cmd.Dir = w.stackDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, TFError(out)
	}
	return out, nil
}

// RefreshOnly refresh Terraform State
func (w *WorkSpace) RefreshOnly(ctx context.Context) (*StateRepresentation, error) {
	chdir := fmt.Sprintf("-chdir=%s", w.tfCacheDir)
	err := w.CleanAndInitWorkspace(ctx)
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, "terraform", chdir, "apply", "-auto-approve", "-json", "--refresh-only", "-lock=false")
	cmd.Dir = w.stackDir

	envs, err := w.initEnvs()
	if err != nil {
		return nil, err
	}
	cmd.Env = envs

	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, TFError(out)
	}
	s, err := w.ShowState(ctx)
	if err != nil {
		return nil, fmt.Errorf("terraform read state error: %v", err)
	}
	return s, err
}

// Destroy make terraform destroy call.
func (w *WorkSpace) Destroy(ctx context.Context) error {
	chdir := fmt.Sprintf("-chdir=%s", w.tfCacheDir)
	cmd := exec.CommandContext(ctx, "terraform", chdir, "destroy", "-auto-approve")
	cmd.Dir = w.stackDir
	envs, err := w.initEnvs()
	if err != nil {
		return err
	}
	cmd.Env = envs
	out, err := cmd.CombinedOutput()
	if err != nil {
		return TFError(out)
	}
	return nil
}

// GetProvider get provider addr from terraform lock file.
// return provider addr and errors
// eg. registry.terraform.io/hashicorp/local/2.2.3
func (w *WorkSpace) GetProvider() (string, error) {
	parser := hclparse.NewParser()
	hclFile, diags := parser.ParseHCLFile(filepath.Join(w.tfCacheDir, LockHCLFile))
	if diags != nil {
		return "", errors.New(diags.Error())
	}
	body := hclFile.Body
	content, diags := body.Content(&hcl.BodySchema{
		Blocks: []hcl.BlockHeaderSchema{
			{
				Type:       "provider",
				LabelNames: []string{"source_addr"},
			},
		},
	})
	if diags != nil {
		return "", errors.New(diags.Error())
	}
	rawAddr := content.Blocks[0].Labels[0]

	block := content.Blocks[0]
	providerVersion, _ := block.Body.Content(&hcl.BodySchema{
		Attributes: []hcl.AttributeSchema{
			{Name: "version", Required: true},
			{Name: "constraints"},
			{Name: "hashes"},
		},
	})
	expr := providerVersion.Attributes["version"].Expr
	var rawVersion string
	diags = gohcl.DecodeExpression(expr, nil, &rawVersion)
	if diags != nil {
		return "", errors.New(diags.Error())
	}

	providerAddr := fmt.Sprintf("%s/%s", rawAddr, rawVersion)
	return providerAddr, nil
}

// CleanAndInitWorkspace will clean up the provider cache and reinitialize the workspace
// when the provider version or hash is updated.
func (w *WorkSpace) CleanAndInitWorkspace(ctx context.Context) error {
	isVersionUpdate, err := w.checkVersionUpdate()
	if err != nil {
		return fmt.Errorf("check provider version failed: %v", err)
	}

	// If the provider hash or version changes, delete the tf cache and reinitialize.
	if isVersionUpdate {
		log.Info("provider hash or version change.")
		err = os.Remove(filepath.Join(w.tfCacheDir, LockHCLFile))
		if err != nil {
			return err
		}
		err = os.RemoveAll(filepath.Join(w.tfCacheDir, ".terraform"))
		if err != nil {
			return err
		}
		err = w.InitWorkSpace(ctx)
		if err != nil {
			return fmt.Errorf("init terraform workspace failed: %v", err)
		}
	}
	return nil
}

// checkVersionUpdate checks whether the provider version has changed, and returns true if changed
func (w *WorkSpace) checkVersionUpdate() (bool, error) {
	providerAddr, err := w.GetProvider()
	if err != nil {
		return false, fmt.Errorf("provider get version failed: %v", err)
	}
	return providerAddr != w.resource.Extensions["provider"].(string), nil
}

// getProviderLogPath returns the provider log path environmental variable,
// the environmental variables that determine the provider log go to a file.
func (w *WorkSpace) getEnvProviderLogPath() (string, error) {
	kusionDataDir, err := kfile.KusionDataFolder()
	if err != nil {
		return "", err
	}
	if v := os.Getenv("LOG_DIR"); v != "" {
		kusionDataDir = v
	}
	provider := strings.Split(w.resource.Extensions["provider"].(string), "/")
	providerLogPath := filepath.Join(kusionDataDir, "logs", fmt.Sprintf("%s-%s.log", tfProviderPrefix, provider[len(provider)-2]))
	envTFLogPath := fmt.Sprintf("%s=%s", envLogPath, providerLogPath)
	return envTFLogPath, nil
}

func getProviderCachePath() (string, error) {
	curUser, err := user.Current()
	if err != nil {
		return "", err
	}

	cachePath := filepath.Join(curUser.HomeDir, terraformD, pluginCache)
	err = io.CreateDirIfNotExist(cachePath)
	if err != nil {
		return "", err
	}
	envTFPluginCache := fmt.Sprintf("%s=%s", envPluginCacheDir, cachePath)
	return envTFPluginCache, nil
}
