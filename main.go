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
	"github.com/containers/common/pkg/config"
	"github.com/containers/image/v5/transports/alltransports"
	"github.com/containers/storage"
	"github.com/containers/storage/pkg/unshare"
	"github.com/sirupsen/logrus"
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
		return
	}
	unshare.MaybeReexecUsingUserNamespace(false)

	logger := logrus.New()
	logger.Level = logrus.DebugLevel
	buildStoreOptions, err := storage.DefaultStoreOptionsAutoDetectUID()
	if err != nil {
		fmt.Printf("%v", err)
		os.Exit(1)
	}

	// fmt.Printf("buildStoreOptions: \n%v\n", buildStoreOptions)

	conf, err := config.Default()
	if err != nil {
		fmt.Printf("%v", err)
		os.Exit(1)
	}

	// fmt.Printf("conf: \n%v\n", conf)

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
		Logger:       logger,
	}
	builder, err := buildah.NewBuilder(context.TODO(), buildStore, builderOpts)
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

	graphDataUntarFolder := "graph-data-untarred"
	err = untar(graphDataArchive, graphDataUntarFolder)
	if err != nil {
		fmt.Printf("%v", err)
		os.Exit(1)
	}
	addOptions := buildah.AddAndCopyOptions{Chown: "0:0", PreserveOwnership: false}
	addErr := builder.Add(graphDataDir, false, addOptions, graphDataUntarFolder)
	if addErr != nil {
		fmt.Printf("%v", addErr)
		os.Exit(1)
	}
	fmt.Printf("after adding the layer")
	builder.SetCmd([]string{"/bin/bash", "-c", fmt.Sprintf("exec cp -rp %s/* %s", graphDataDir, graphDataMountPath)})
	imageRef, err := alltransports.ParseImageName("docker://localhost:7000/" + graphImageName)
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
