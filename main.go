package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"archive/tar"
	"compress/gzip"

	"github.com/containers/buildah"
	"github.com/containers/buildah/pkg/parse"
	"github.com/containers/common/pkg/config"
	is "github.com/containers/image/v5/storage"
	"github.com/containers/storage"
	"github.com/containers/storage/pkg/unshare"
)

const (
	graphPreparationDir         string = "graph-preparation"
	graphBaseImage              string = "registry.access.redhat.com/ubi9/ubi:latest"
	graphURL                    string = "https://api.openshift.com/api/upgrades_info/graph-data"
	outputFile                  string = "cincinnati-graph-data.tar.gz"
	graphDataDir                string = "/var/lib/cincinnati-graph-data/"
	graphDataMountPath          string = "/var/lib/cincinnati/graph-data"
	graphImageName              string = "graph-image"
	indexJson                   string = "manifest.json"
	operatorImageExtractDir     string = "hold-operator"
	workingDir                  string = "working-dir"
	dockerProtocol              string = "docker://"
	ociProtocol                 string = "oci://"
	ociProtocolTrimmed          string = "oci:"
	dirProtocol                 string = "dir://"
	dirProtocolTrimmed          string = "dir:"
	releaseImageDir             string = "release-images"
	releaseIndex                string = "release-index"
	releaseFiltersDir           string = "release-filters"
	operatorImageDir            string = "operator-images"
	releaseImageExtractDir      string = "hold-release"
	releaseManifests            string = "release-manifests"
	imageReferences             string = "image-references"
	releaseImageExtractFullPath string = releaseManifests + "/" + imageReferences
	blobsDir                    string = "blobs/sha256"
	errMsg                      string = "[ReleaseImageCollector] %v "
	diskToMirror                string = "diskToMirror"
	mirrorToDisk                string = "mirrorToDisk"
	prepare                     string = "prepare"
	logFile                     string = "logs/release.log"
)

func untar(src, dest string) error {
	file, err := os.Open(src)
	if err != nil {
		return err
	}
	defer file.Close()

	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer gzipReader.Close()

	tarReader := tar.NewReader(gzipReader)

	for {
		header, err := tarReader.Next()

		if err == io.EOF {
			break
		}

		if err != nil {
			return err
		}

		path := filepath.Join(dest, header.Name)

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(path, 0755); err != nil {
				return err
			}
		case tar.TypeReg:
			file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, os.FileMode(header.Mode))
			if err != nil {
				return err
			}
			defer file.Close()

			if _, err := io.Copy(file, tarReader); err != nil {
				return err
			}
		}
	}

	return nil
}

func main() {
	// inspired from https://github.com/containers/buildah/blob/main/docs/tutorials/04-include-in-your-build-tool.md
	if buildah.InitReexec() {
		os.Exit(1)
	}
	unshare.MaybeReexecUsingUserNamespace(false)

	buildStoreOptions, err := storage.DefaultStoreOptionsAutoDetectUID()
	//buildStoreOptions, err := storage.DefaultStoreOptions(unshare.GetRootlessUID() > 0, unshare.GetRootlessUID())
	if err != nil {
		fmt.Printf("%v", err)
		os.Exit(1)
	}

	conf, err := config.Default()
	if err != nil {
		fmt.Printf("%v", err)
		os.Exit(1)
	}
	capabilitiesForRoot, err := conf.Capabilities("root", nil, nil)
	if err != nil {
		fmt.Printf("%v", err)
		os.Exit(1)
	}
	buildStore, err := storage.GetStore(buildStoreOptions)
	if err != nil {
		fmt.Printf("%v", err)
		os.Exit(1)
	}
	defer buildStore.Shutdown(false)

	builderOpts := buildah.BuilderOptions{
		FromImage:    graphBaseImage,
		Capabilities: capabilitiesForRoot,
	}
	builder, err := buildah.NewBuilder(context.TODO(), buildStore, builderOpts)
	if err != nil {
		fmt.Printf("%v", err)
		os.Exit(1)
	}

	isolation, err := parse.IsolationOption("")
	if err != nil {
		fmt.Printf("%v", err)
		os.Exit(1)
	}

	// HTTP Get the graph updates from api endpoint
	resp, err := http.Get(graphURL)
	if err != nil {
		fmt.Printf("%v", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		fmt.Printf("%v", err)
		os.Exit(1)
	}

	// save graph data to tar.gz file
	graphDataArchive := outputFile
	err = os.WriteFile(graphDataArchive, body, 0644)
	if err != nil {
		fmt.Printf("%v", err)
		os.Exit(1)
	}

	// According to https://docs.openshift.com/container-platform/4.13/updating/updating-restricted-network-cluster/restricted-network-update-osus.html#update-service-graph-data_updating-restricted-network-cluster-osus
	//mkdir -p /var/lib/cincinnati-graph-data  && tar xvzf cincinnati-graph-data.tar.gz -C /var/lib/cincinnati-graph-data/ --no-overwrite-dir --no-same-owner
	err = builder.Run([]string{"sh", "-c", "mkdir -p /var/lib/cincinnati-graph-data  && tar xvzf cincinnati-graph-data.tar.gz -C /var/lib/cincinnati-graph-data/ --no-overwrite-dir --no-same-owner"}, buildah.RunOptions{Isolation: isolation, Terminal: buildah.WithoutTerminal})
	// also tried
	// err = builder.Run([]string{"sh", "-c", "ls "}, buildah.RunOptions{Isolation: isolation, Terminal: buildah.WithoutTerminal})
	// but didnt work either
	if err != nil {
		fmt.Printf("%v", err)
		os.Exit(1)
	}
	//this also failed: use `untar` to save to graphDataUntarFolder , then add the folder
	// err = builder.Add(graphDataDir, false, addOptions, graphDataUntarFolder)
	// if err != nil {
	// 	fmt.Printf("%v", err)
	// 	os.Exit(1)
	// }
	// fmt.Printf("after adding the layer")
	builder.SetCmd([]string{"/bin/bash", "-c", fmt.Sprintf("exec cp -rp %s/* %s", graphDataDir, graphDataMountPath)})
	imageRef, err := is.Transport.ParseStoreReference(buildStore, "docker://localhost:5000/"+graphImageName)
	if err != nil {
		fmt.Printf("%v", err)
		os.Exit(1)
	}

	imageId, _, _, err := builder.Commit(context.TODO(), imageRef, buildah.CommitOptions{})
	if err != nil {
		fmt.Printf("%v", err)
		os.Exit(1)
	}
	fmt.Printf("Image ID: %s\n", imageId)
}
