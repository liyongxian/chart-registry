package storage

import (
	"bytes"
	"errors"
	"github.com/deislabs/oras/pkg/auth/docker"
	"github.com/hangyan/chart-registry/pkg/storage/registry"
	registryclient "github.com/heroku/docker-registry-client/registry"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/helmpath"
	"io/ioutil"
	"k8s.io/klog"
	"os"
	pathutil "path"
	"strings"
)

var (
	objectCache map[string]Object
)

type RegistryBackend struct {
	Client registry.Client

	Repo string

	CacheRoot string

	Hub *registryclient.Registry
}

func createObjectMap(objects []Object) {
	objectCache = map[string]Object{}
	for _, item := range objects {
		objectCache[item.Path] = item
	}
}


func NewRegistryBackend(repo string) *RegistryBackend {

	client, err := docker.NewClient()
	if err != nil {
		panic(err)
	}

	clientOpts := []registry.ClientOption{
		registry.ClientOptDebug(true),
		registry.ClientOptWriter(os.Stdout),
		registry.ClientOptAuthorizer(&registry.Authorizer{Client: client}),
	}

	regClient, err := registry.NewClient(clientOpts...)
	if err != nil {
		panic(err)
	}

	// distribution client
	url := repo
	if !strings.HasPrefix(repo, "http") {
		url = "http://" + repo
	}
	hub, err := registryclient.NewInsecure(url, "", "")
	if err != nil {
		panic(err)
	}

	return &RegistryBackend{
		Client:    *regClient,
		Repo:      repo,
		Hub: hub,
		CacheRoot: helmpath.CachePath("registry", registry.CacheRootDir),
	}

}


func (b *RegistryBackend) listObjects(prefix string) ([]Object, error) {
	repositories, err := b.Hub.Repositories()
	if err != nil {
		return nil, err
	}
	klog.Infof("get repo list: %+v", repositories)

	for _, repo := range repositories {
		tags, err := b.Hub.Tags(repo)
		if err != nil {
			return nil, err
		}
		for _, tag := range tags {
			klog.Infof("get manifest for: %s %s", repo, tag)
			manifest, err := b.Hub.Manifest(repo, tag)
			if err != nil {
				klog.Error("fuck error:", err)
				return nil, err
			}
			klog.Infof("fuck manifest: %+v", manifest)
		}
	}

	return nil, nil
}


func (b *RegistryBackend) ListObjects(prefix string) ([]Object, error) {
	klog.Info("List objects with prefix: ", prefix)

	b.listObjects(prefix)


	objects, err := b.Client.ListCharts()
	if err != nil {
		return nil, err
	}
	var result []Object
	for _, item := range objects {
		result = append(result, *NewObject(item))

	}
	klog.Infof("Retrieve %d objects from storage", len(result))
	createObjectMap(result)
	return result, nil
}

func (b *RegistryBackend) GetObject(path string) (Object, error) {
	var object Object
	if path == "index-cache.yaml" {
		klog.Infof("Retrieve index cache ")
		object.Path = path
		fullpath := pathutil.Join(b.CacheRoot, path)
		content, err := ioutil.ReadFile(fullpath)
		if err != nil {
			return object, err
		}
		object.Content = content
		info, err := os.Stat(fullpath)
		if err != nil {
			return object, err
		}
		object.LastModified = info.ModTime()
		return object, err
	}

	klog.Infof("Retrieve object: %s", path)

	// old ref
	//name, version := parseChartName(path)
	// ref := b.Repo + "/" + name + ":" + version

	// new ref
	obj := objectCache[path]
	ref := obj.Name
	if ref != "" {
		klog.Infof("get ref for path: %s -> %s", path, obj.Name)
	} else {
		return object, errors.New("cannot get ref for path")
	}

	r, err := registry.ParseReference(ref)
	if err != nil {
		return object, err
	}

	chart, err := b.Client.LoadChart(r)
	if err != nil {
		return object, err
	}

	return *NewObject(chart), nil
}

func (b *RegistryBackend) GenFullName(path string) string {
	name, version := parseChartName(path)
	return b.Repo + "/" + name + ":" + version

}

func (b *RegistryBackend) PutObject(path string, content []byte) error {
	if path == "index-cache.yaml" {
		klog.Infof("Update index cache")
		fullpath := pathutil.Join(b.CacheRoot, path)
		folderPath := pathutil.Dir(fullpath)
		_, err := os.Stat(folderPath)
		if err != nil {
			if os.IsNotExist(err) {
				err := os.MkdirAll(folderPath, 0777)
				if err != nil {
					return err
				}
			} else {
				return err
			}
		}
		err = ioutil.WriteFile(fullpath, content, 0644)
		return err
	}

	klog.Infof("Update chart: %s", path)

	ref := b.GenFullName(path)
	klog.Infof("Registry path is : %s", ref)
	reader := bytes.NewReader(content)
	ch, err := loader.LoadArchive(reader)
	if err != nil {
		klog.Error("load chart error: ", err)
		return err
	}

	// ref = "myrepo:5001/mychart:1.5.0"
	//TODO: fix can use ip
	// klog.Infof("Registry path is : %s", ref)

	r, err := registry.ParseReference(ref)
	if err != nil {
		klog.Error("Parse ref error: ", err)
		return err
	}

	// If no tag is present, use the chart version
	if r.Tag == "" {
		r.Tag = ch.Metadata.Version
	}

	if err := b.Client.SaveChart(ch, r); err != nil {
		klog.Errorf("Save chart error: %s %s", r.FullName(), err.Error())
		return err
	}

	if err := b.Client.PushChart(r); err != nil {
		klog.Errorf("Push chart error: %s %s", r.FullName(), err.Error())
		return err
	}

	return nil
}

func (*RegistryBackend) DeleteObject(path string) error {
	panic("implement me")
}

func parseChartName(name string) (string, string) {
	splits := strings.Split(name, ".")
	name = name[:len(name)-len(splits[len(splits)-1])-1]
	splits = strings.Split(name, "-")
	l := len(splits)
	version := splits[l-1]
	name = name[:len(name)-len(version)-1]
	return name, version
}
