package installed

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"strings"
	"testing"
	"testing/fstest"

	"forge.cadoles.com/vcarroy/arkhitekton/sdk/capability"
	"forge.cadoles.com/vcarroy/arkhitekton/sdk/manifest"
)

const withProgram = `apiVersion: arkhitekton.plugin/v2
id: deploy
version: 2.0.0
sdk: "1"
label: Deploy
binary: bin/plugin
protocol: 1
module: true
migrations: { dir: migrations, table: goose_deploy_db_version }
routes:
  - { method: GET, path: environments, permission: read }
ui: { entry: ui/index.js, resources: [ui/screens/*.yaml] }
`

const environmentsScreen = `apiVersion: ui.arkhitekton/v1
kind: Screen
metadata: { name: deploy.environments }
spec:
  path: environments
  parent: shell
  blocks: [{ id: deploy.list, component: deploy/environments }]
`

// programPlugin is a plugin directory with a program per platform, as publish-plugin finds it.
func programPlugin() fstest.MapFS {
	return fstest.MapFS{
		manifest.FileName:                  {Data: []byte(withProgram)},
		"bin/plugin-linux-amd64":           {Data: []byte("program for amd64")},
		"bin/plugin-linux-arm64":           {Data: []byte("program for arm64")},
		"migrations/0001_environments.sql": {Data: []byte("-- +goose Up\nselect 1;\n")},
		"ui/index.js":                      {Data: []byte("export default {}")},
		"ui/screens/environments.yaml":     {Data: []byte(environmentsScreen)},
	}
}

// platform is the directory as a platform's artifact holds it.
func platform(architecture string) fstest.MapFS {
	files := programPlugin()
	files["bin/plugin"] = files["bin/plugin-linux-"+architecture]
	delete(files, "bin/plugin-linux-amd64")
	delete(files, "bin/plugin-linux-arm64")
	return files
}

func TestAProgramIsReadWithWhatItBrings(t *testing.T) {
	loaded, err := Load(platform("amd64"))
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Native == nil {
		t.Fatal("no program")
	}
	sum := sha256.Sum256([]byte("program for amd64"))
	if loaded.Native.Digest != "sha256:"+hex.EncodeToString(sum[:]) {
		t.Errorf("digest = %s", loaded.Native.Digest)
	}
	program, err := loaded.Native.Program()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = program.Close() }()
	if raw, _ := io.ReadAll(program); string(raw) != "program for amd64" {
		t.Errorf("program = %q", raw)
	}
	if len(loaded.Capabilities) != 1 || loaded.Capabilities[0].Kind != capability.KindModule {
		t.Errorf("capabilities = %+v", loaded.Capabilities)
	}
	if resources := loaded.Native.Interface; len(resources) != 1 || resources[0].Name != "deploy.environments" {
		t.Errorf("interface = %+v", resources)
	}
}

func TestAProgramMissingWhatItDeclaresIsRefused(t *testing.T) {
	for name, remove := range map[string]string{
		"program":    "bin/plugin",
		"migrations": "migrations/0001_environments.sql",
		"interface":  "ui/index.js",
		"screens":    "ui/screens/environments.yaml",
	} {
		files := platform("amd64")
		delete(files, remove)
		if _, err := Load(files); err == nil {
			t.Errorf("without its %s: accepted", name)
		}
	}
}

func TestAnInterfaceResourceThatDoesNotReadIsRefused(t *testing.T) {
	files := platform("amd64")
	files["ui/screens/environments.yaml"] = &fstest.MapFile{Data: []byte(strings.Replace(environmentsScreen, "component: deploy/environments", "component: nothing", 1))}
	if _, err := Load(files); err == nil || !strings.Contains(err.Error(), "environments.yaml") {
		t.Errorf("err = %v", err)
	}
}

// A pipeline the program reads is checked without the program; read for good, it needs a role
// that runs programs.
func TestAPipelineReadByTheProgram(t *testing.T) {
	files := plugin(t)

	if _, err := Load(files); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(files, Programs{}); err == nil || !strings.Contains(err.Error(), "does not run") {
		t.Errorf("read without programs: %v", err)
	}
	asked := ""
	read, err := Read(files, Programs{Interpreter: func(plugin capability.Plugin, pipeline string) capability.Interpreter {
		asked = plugin.ID + "/" + pipeline
		return notRunning{}
	}})
	if err != nil || asked != "gotest/unit-tests" || read.Pipelines[0].Interpreter == nil {
		t.Errorf("read %v, asked %q", err, asked)
	}
}

func TestAProgramIsPublishedPerPlatform(t *testing.T) {
	host, registries := registryHost(t)
	key, trusted := ecdsaKey(t)
	reference, err := Publish(context.Background(), registries, host+"/plugins/deploy:2.0.0", programPlugin(), key)
	if err != nil {
		t.Fatal(err)
	}
	for _, architecture := range Platforms {
		registries.Architecture = architecture
		pulled, err := Pull(context.Background(), registries, trusted, nil, false, reference)
		if err != nil {
			t.Fatalf("%s: %v", architecture, err)
		}
		loaded, err := Load(pulled.Files)
		if err != nil {
			t.Fatalf("%s: %v", architecture, err)
		}
		program, _ := loaded.Native.Program()
		raw, _ := io.ReadAll(program)
		_ = program.Close()
		if string(raw) != "program for "+architecture {
			t.Errorf("%s pulled %q", architecture, raw)
		}
		if _, found := pulled.Files.(fstest.MapFS)["bin/plugin-linux-amd64"]; found {
			t.Errorf("%s: the other platforms' programs travel too", architecture)
		}
	}

	registries.Architecture = "riscv64"
	if _, err := Pull(context.Background(), registries, trusted, nil, false, reference); err == nil || !strings.Contains(err.Error(), "linux/riscv64") {
		t.Errorf("an absent platform: %v", err)
	}
}

func TestAProgramBuiltForNoPlatformIsNotPublished(t *testing.T) {
	host, registries := registryHost(t)
	key, _ := ecdsaKey(t)
	files := programPlugin()
	delete(files, "bin/plugin-linux-amd64")
	delete(files, "bin/plugin-linux-arm64")
	if _, err := Publish(context.Background(), registries, host+"/plugins/deploy:2.0.0", files, key); err == nil ||
		!strings.Contains(err.Error(), "bin/plugin-linux-") {
		t.Errorf("err = %v", err)
	}
}

func TestARouteNeedsAPermissionSomeRoleGrants(t *testing.T) {
	files := platform("amd64")
	files[manifest.FileName] = &fstest.MapFile{Data: []byte(strings.Replace(withProgram, "permission: read", "permission: reed", 1))}
	if _, err := Load(files); err == nil || !strings.Contains(err.Error(), `"reed"`) {
		t.Errorf("err = %v", err)
	}
}
