package v1alpha1

import (
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"os"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/watch"
	"sigs.k8s.io/yaml"
)

const AnnotationKeyLinkPrefix string = "link.argocd.argoproj.io/"
const AnnotationDeletionApproved string = "argocd.argoproj.io/deletion-approved"

type ResultCode string

const (
	ResultCodeSynced       ResultCode = "Synced"
	ResultCodeSyncFailed   ResultCode = "SyncFailed"
	ResultCodePruned       ResultCode = "Pruned"
	ResultCodePruneSkipped ResultCode = "PruneSkipped"
)

type HookType string

const (
	HookTypePreSync  HookType = "PreSync"
	HookTypeSync     HookType = "Sync"
	HookTypePostSync HookType = "PostSync"
	HookTypeSkip     HookType = "Skip"
	HookTypeSyncFail HookType = "SyncFail"
)

func NewHookType(t string) (HookType, bool) {
	return HookType(t),
		t == string(HookTypePreSync) ||
			t == string(HookTypeSync) ||
			t == string(HookTypePostSync) ||
			t == string(HookTypeSyncFail) ||
			t == string(HookTypeSkip)
}

type OperationPhase string

const (
	OperationRunning     OperationPhase = "Running"
	OperationTerminating OperationPhase = "Terminating"
	OperationFailed      OperationPhase = "Failed"
	OperationError       OperationPhase = "Error"
	OperationSucceeded   OperationPhase = "Succeeded"
)

func (os OperationPhase) Completed() bool {
	switch os {
	case OperationFailed, OperationError, OperationSucceeded:
		return true
	}
	return false
}

func (os OperationPhase) Running() bool {
	return os == OperationRunning
}

func (os OperationPhase) Successful() bool {
	return os == OperationSucceeded
}

func (os OperationPhase) Failed() bool {
	return os == OperationFailed
}

type ResourceKey struct {
	Group     string
	Kind      string
	Namespace string
	Name      string
}

func (k *ResourceKey) String() string {
	return fmt.Sprintf("%s/%s/%s/%s", k.Group, k.Kind, k.Namespace, k.Name)
}

func (k ResourceKey) GroupKind() schema.GroupKind {
	return schema.GroupKind{Group: k.Group, Kind: k.Kind}
}

func NewResourceKey(group string, kind string, namespace string, name string) ResourceKey {
	return ResourceKey{Group: group, Kind: kind, Namespace: namespace, Name: name}
}

func GetResourceKey(obj *unstructured.Unstructured) ResourceKey {
	gvk := obj.GroupVersionKind()
	return NewResourceKey(gvk.Group, gvk.Kind, obj.GetNamespace(), obj.GetName())
}

// Application is a definition of Application resource.
// +genclient
// +genclient:noStatus
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:resource:path=applications,shortName=app;apps
// +kubebuilder:printcolumn:name="Sync Status",type=string,JSONPath=`.status.sync.status`
// +kubebuilder:printcolumn:name="Health Status",type=string,JSONPath=`.status.health.status`
// +kubebuilder:printcolumn:name="Revision",type=string,JSONPath=`.status.sync.revision`,priority=10
// +kubebuilder:printcolumn:name="Project",type=string,JSONPath=`.spec.project`,priority=10
type Application struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata" protobuf:"bytes,1,opt,name=metadata"`
	Spec              ApplicationSpec   `json:"spec" protobuf:"bytes,2,opt,name=spec"`
	Status            ApplicationStatus `json:"status,omitempty" protobuf:"bytes,3,opt,name=status"`
	Operation         *Operation        `json:"operation,omitempty" protobuf:"bytes,4,opt,name=operation"`
}

// ApplicationSpec represents desired application state. Contains link to repository with application definition and additional parameters link definition revision.
type ApplicationSpec struct {
	// Source is a reference to the location of the application's manifests or chart
	Source *ApplicationSource `json:"source,omitempty" protobuf:"bytes,1,opt,name=source"`
	// Destination is a reference to the target Kubernetes server and namespace
	Destination ApplicationDestination `json:"destination" protobuf:"bytes,2,name=destination"`
	// Project is a reference to the project this application belongs to.
	// The empty string means that application belongs to the 'default' project.
	Project string `json:"project" protobuf:"bytes,3,name=project"`
	// SyncPolicy controls when and how a sync will be performed
	SyncPolicy *SyncPolicy `json:"syncPolicy,omitempty" protobuf:"bytes,4,name=syncPolicy"`
	// IgnoreDifferences is a list of resources and their fields which should be ignored during comparison
	IgnoreDifferences IgnoreDifferences `json:"ignoreDifferences,omitempty" protobuf:"bytes,5,name=ignoreDifferences"`
	// Info contains a list of information (URLs, email addresses, and plain text) that relates to the application
	Info []Info `json:"info,omitempty" protobuf:"bytes,6,name=info"`
	// RevisionHistoryLimit limits the number of items kept in the application's revision history, which is used for informational purposes as well as for rollbacks to previous versions.
	// This should only be changed in exceptional circumstances.
	// Setting to zero will store no history. This will reduce storage used.
	// Increasing will increase the space used to store the history, so we do not recommend increasing it.
	// Default is 10.
	RevisionHistoryLimit *int64 `json:"revisionHistoryLimit,omitempty" protobuf:"bytes,7,name=revisionHistoryLimit"`

	// Sources is a reference to the location of the application's manifests or chart
	Sources ApplicationSources `json:"sources,omitempty" protobuf:"bytes,8,opt,name=sources"`
}

type IgnoreDifferences []ResourceIgnoreDifferences

func (id IgnoreDifferences) Equals(other IgnoreDifferences) bool {
	return reflect.DeepEqual(id, other)
}

type TrackingMethod string

const (
	TrackingMethodAnnotation         TrackingMethod = "annotation"
	TrackingMethodLabel              TrackingMethod = "label"
	TrackingMethodAnnotationAndLabel TrackingMethod = "annotation+label"
)

// ResourceIgnoreDifferences contains resource filter and list of json paths which should be ignored during comparison with live state.
type ResourceIgnoreDifferences struct {
	Group             string   `json:"group,omitempty" protobuf:"bytes,1,opt,name=group"`
	Kind              string   `json:"kind" protobuf:"bytes,2,opt,name=kind"`
	Name              string   `json:"name,omitempty" protobuf:"bytes,3,opt,name=name"`
	Namespace         string   `json:"namespace,omitempty" protobuf:"bytes,4,opt,name=namespace"`
	JSONPointers      []string `json:"jsonPointers,omitempty" protobuf:"bytes,5,opt,name=jsonPointers"`
	JQPathExpressions []string `json:"jqPathExpressions,omitempty" protobuf:"bytes,6,opt,name=jqPathExpressions"`
	// ManagedFieldsManagers is a list of trusted managers. Fields mutated by those managers will take precedence over the
	// desired state defined in the SCM and won't be displayed in diffs
	ManagedFieldsManagers []string `json:"managedFieldsManagers,omitempty" protobuf:"bytes,7,opt,name=managedFieldsManagers"`
}

// EnvEntry represents an entry in the application's environment
type EnvEntry struct {
	// Name is the name of the variable, usually expressed in uppercase
	Name string `json:"name" protobuf:"bytes,1,opt,name=name"`
	// Value is the value of the variable
	Value string `json:"value" protobuf:"bytes,2,opt,name=value"`
}

// IsZero returns true if a variable is considered empty or unset
func (a *EnvEntry) IsZero() bool {
	return a == nil || a.Name == "" && a.Value == ""
}

// NewEnvEntry parses a string in format name=value and returns an EnvEntry object
func NewEnvEntry(text string) (*EnvEntry, error) {
	parts := strings.SplitN(text, "=", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("Expected env entry of the form: param=value. Received: %s", text)
	}
	return &EnvEntry{
		Name:  parts[0],
		Value: parts[1],
	}, nil
}

// Env is a list of environment variable entries
type Env []*EnvEntry

// IsZero returns true if a list of variables is considered empty
func (e Env) IsZero() bool {
	return len(e) == 0
}

// Environ returns a list of environment variables in name=value format from a list of variables
func (e Env) Environ() []string {
	var environ []string
	for _, item := range e {
		if !item.IsZero() {
			environ = append(environ, fmt.Sprintf("%s=%s", item.Name, item.Value))
		}
	}
	return environ
}

// Envsubst interpolates variable references in a string from a list of variables
func (e Env) Envsubst(s string) string {
	valByEnv := map[string]string{}
	for _, item := range e {
		valByEnv[item.Name] = item.Value
	}
	return os.Expand(s, func(s string) string {
		// allow escaping $ with $$
		if s == "$" {
			return "$"
		}
		return valByEnv[s]
	})
}

// ApplicationSource contains all required information about the source of an application
type ApplicationSource struct {
	// RepoURL is the URL to the repository (Git or Helm) that contains the application manifests
	RepoURL string `json:"repoURL" protobuf:"bytes,1,opt,name=repoURL"`
	// Path is a directory path within the Git repository, and is only valid for applications sourced from Git.
	Path string `json:"path,omitempty" protobuf:"bytes,2,opt,name=path"`
	// TargetRevision defines the revision of the source to sync the application to.
	// In case of Git, this can be commit, tag, or branch. If omitted, will equal to HEAD.
	// In case of Helm, this is a semver tag for the Chart's version.
	TargetRevision string `json:"targetRevision,omitempty" protobuf:"bytes,4,opt,name=targetRevision"`
	// Helm holds helm specific options
	Helm *ApplicationSourceHelm `json:"helm,omitempty" protobuf:"bytes,7,opt,name=helm"`
	// Kustomize holds kustomize specific options
	Kustomize *ApplicationSourceKustomize `json:"kustomize,omitempty" protobuf:"bytes,8,opt,name=kustomize"`
	// Directory holds path/directory specific options
	Directory *ApplicationSourceDirectory `json:"directory,omitempty" protobuf:"bytes,10,opt,name=directory"`
	// Plugin holds config management plugin specific options
	Plugin *ApplicationSourcePlugin `json:"plugin,omitempty" protobuf:"bytes,11,opt,name=plugin"`
	// Chart is a Helm chart name, and must be specified for applications sourced from a Helm repo.
	Chart string `json:"chart,omitempty" protobuf:"bytes,12,opt,name=chart"`
	// Ref is reference to another source within sources field. This field will not be used if used with a `source` tag.
	Ref string `json:"ref,omitempty" protobuf:"bytes,13,opt,name=ref"`
	// Name is used to refer to a source and is displayed in the UI. It is used in multi-source Applications.
	Name string `json:"name,omitempty" protobuf:"bytes,14,opt,name=name"`
}

// ApplicationSources contains list of required information about the sources of an application
type ApplicationSources []ApplicationSource

func (a ApplicationSources) Equals(other ApplicationSources) bool {
	if len(a) != len(other) {
		return false
	}
	for i := range a {
		if !a[i].Equals(&other[i]) {
			return false
		}
	}
	return true
}

// IsZero returns true if the application source is considered empty
func (a ApplicationSources) IsZero() bool {
	return len(a) == 0
}

func (spec *ApplicationSpec) GetSource() ApplicationSource {
	// if Application has multiple sources, return the first source in sources
	if spec.HasMultipleSources() {
		return spec.Sources[0]
	}
	if spec.Source != nil {
		return *spec.Source
	}
	return ApplicationSource{}
}

func (spec *ApplicationSpec) GetSources() ApplicationSources {
	if spec.HasMultipleSources() {
		return spec.Sources
	}
	if spec.Source != nil {
		return ApplicationSources{*spec.Source}
	}
	return ApplicationSources{}
}

func (spec *ApplicationSpec) HasMultipleSources() bool {
	return len(spec.Sources) > 0
}

func (spec *ApplicationSpec) GetSourcePtrByPosition(sourcePosition int) *ApplicationSource {
	// if Application has multiple sources, return the first source in sources
	return spec.GetSourcePtrByIndex(sourcePosition - 1)
}

func (spec *ApplicationSpec) GetSourcePtrByIndex(sourceIndex int) *ApplicationSource {
	// if Application has multiple sources, return the first source in sources
	if spec.HasMultipleSources() {
		if sourceIndex > 0 {
			return &spec.Sources[sourceIndex]
		}
		return &spec.Sources[0]
	}
	return spec.Source
}

// AllowsConcurrentProcessing returns true if given application source can be processed concurrently
func (source *ApplicationSource) AllowsConcurrentProcessing() bool {
	// Kustomize with parameters requires changing kustomization.yaml file
	if source.Kustomize != nil {
		return source.Kustomize.AllowsConcurrentProcessing()
	}
	return true
}

// IsRef returns true when the application source is of type Ref
func (source *ApplicationSource) IsRef() bool {
	return source.Ref != ""
}

// IsHelm returns true when the application source is of type Helm
func (source *ApplicationSource) IsHelm() bool {
	return source.Chart != ""
}

// IsZero returns true if the application source is considered empty
func (source *ApplicationSource) IsZero() bool {
	return source == nil ||
		source.RepoURL == "" &&
			source.Path == "" &&
			source.TargetRevision == "" &&
			source.Helm.IsZero() &&
			source.Kustomize.IsZero() &&
			source.Directory.IsZero() &&
			source.Plugin.IsZero()
}

// GetNamespaceOrDefault gets the static namespace configured in the source. If none is configured, returns the given
// default.
func (source *ApplicationSource) GetNamespaceOrDefault(defaultNamespace string) string {
	if source == nil {
		return defaultNamespace
	}
	if source.Helm != nil && source.Helm.Namespace != "" {
		return source.Helm.Namespace
	}
	if source.Kustomize != nil && source.Kustomize.Namespace != "" {
		return source.Kustomize.Namespace
	}
	return defaultNamespace
}

// GetKubeVersionOrDefault gets the static Kubernetes API version configured in the source. If none is configured,
// returns the given default.
func (source *ApplicationSource) GetKubeVersionOrDefault(defaultKubeVersion string) string {
	if source == nil {
		return defaultKubeVersion
	}
	if source.Helm != nil && source.Helm.KubeVersion != "" {
		return source.Helm.KubeVersion
	}
	if source.Kustomize != nil && source.Kustomize.KubeVersion != "" {
		return source.Kustomize.KubeVersion
	}
	return defaultKubeVersion
}

// GetAPIVersionsOrDefault gets the static API versions list configured in the source. If none is configured, returns
// the given default.
func (source *ApplicationSource) GetAPIVersionsOrDefault(defaultAPIVersions []string) []string {
	if source == nil {
		return defaultAPIVersions
	}
	if source.Helm != nil && len(source.Helm.APIVersions) > 0 {
		return source.Helm.APIVersions
	}
	if source.Kustomize != nil && len(source.Kustomize.APIVersions) > 0 {
		return source.Kustomize.APIVersions
	}
	return defaultAPIVersions
}

// ApplicationSourceType specifies the type of the application's source
type ApplicationSourceType string

const (
	ApplicationSourceTypeHelm      ApplicationSourceType = "Helm"
	ApplicationSourceTypeKustomize ApplicationSourceType = "Kustomize"
	ApplicationSourceTypeDirectory ApplicationSourceType = "Directory"
	ApplicationSourceTypePlugin    ApplicationSourceType = "Plugin"
)

// DrySource specifies a location for dry "don't repeat yourself" manifest source information.
type DrySource struct {
	// RepoURL is the URL to the git repository that contains the application manifests
	RepoURL string `json:"repoURL" protobuf:"bytes,1,name=repoURL"`
	// TargetRevision defines the revision of the source to hydrate
	TargetRevision string `json:"targetRevision" protobuf:"bytes,2,name=targetRevision"`
	// Path is a directory path within the Git repository where the manifests are located
	Path string `json:"path" protobuf:"bytes,3,name=path"`
}

// RefreshType specifies how to refresh the sources of a given application
type RefreshType string

const (
	RefreshTypeNormal RefreshType = "normal"
	RefreshTypeHard   RefreshType = "hard"
)

type HydrateType string

const (
	// HydrateTypeNormal is a normal hydration
	HydrateTypeNormal HydrateType = "normal"
)

// ApplicationSourceHelm holds helm specific options
type ApplicationSourceHelm struct {
	// ValuesFiles is a list of Helm value files to use when generating a template
	ValueFiles []string `json:"valueFiles,omitempty" protobuf:"bytes,1,opt,name=valueFiles"`
	// Parameters is a list of Helm parameters which are passed to the helm template command upon manifest generation
	Parameters []HelmParameter `json:"parameters,omitempty" protobuf:"bytes,2,opt,name=parameters"`
	// ReleaseName is the Helm release name to use. If omitted it will use the application name
	ReleaseName string `json:"releaseName,omitempty" protobuf:"bytes,3,opt,name=releaseName"`
	// Values specifies Helm values to be passed to helm template, typically defined as a block. ValuesObject takes precedence over Values, so use one or the other.
	// +patchStrategy=replace
	Values string `json:"values,omitempty" patchStrategy:"replace" protobuf:"bytes,4,opt,name=values"`
	// FileParameters are file parameters to the helm template
	FileParameters []HelmFileParameter `json:"fileParameters,omitempty" protobuf:"bytes,5,opt,name=fileParameters"`
	// Version is the Helm version to use for templating ("3")
	Version string `json:"version,omitempty" protobuf:"bytes,6,opt,name=version"`
	// PassCredentials pass credentials to all domains (Helm's --pass-credentials)
	PassCredentials bool `json:"passCredentials,omitempty" protobuf:"bytes,7,opt,name=passCredentials"`
	// IgnoreMissingValueFiles prevents helm template from failing when valueFiles do not exist locally by not appending them to helm template --values
	IgnoreMissingValueFiles bool `json:"ignoreMissingValueFiles,omitempty" protobuf:"bytes,8,opt,name=ignoreMissingValueFiles"`
	// SkipCrds skips custom resource definition installation step (Helm's --skip-crds)
	SkipCrds bool `json:"skipCrds,omitempty" protobuf:"bytes,9,opt,name=skipCrds"`
	// ValuesObject specifies Helm values to be passed to helm template, defined as a map. This takes precedence over Values.
	// +kubebuilder:pruning:PreserveUnknownFields
	ValuesObject *runtime.RawExtension `json:"valuesObject,omitempty" protobuf:"bytes,10,opt,name=valuesObject"`
	// Namespace is an optional namespace to template with. If left empty, defaults to the app's destination namespace.
	Namespace string `json:"namespace,omitempty" protobuf:"bytes,11,opt,name=namespace"`
	// KubeVersion specifies the Kubernetes API version to pass to Helm when templating manifests. By default, Argo CD
	// uses the Kubernetes version of the target cluster.
	KubeVersion string `json:"kubeVersion,omitempty" protobuf:"bytes,12,opt,name=kubeVersion"`
	// APIVersions specifies the Kubernetes resource API versions to pass to Helm when templating manifests. By default,
	// Argo CD uses the API versions of the target cluster. The format is [group/]version/kind.
	APIVersions []string `json:"apiVersions,omitempty" protobuf:"bytes,13,opt,name=apiVersions"`
	// SkipTests skips test manifest installation step (Helm's --skip-tests).
	SkipTests bool `json:"skipTests,omitempty" protobuf:"bytes,14,opt,name=skipTests"`
	// SkipSchemaValidation skips JSON schema validation (Helm's --skip-schema-validation)
	SkipSchemaValidation bool `json:"skipSchemaValidation,omitempty" protobuf:"bytes,15,opt,name=skipSchemaValidation"`
}

// HelmParameter is a parameter that's passed to helm template during manifest generation
type HelmParameter struct {
	// Name is the name of the Helm parameter
	Name string `json:"name,omitempty" protobuf:"bytes,1,opt,name=name"`
	// Value is the value for the Helm parameter
	Value string `json:"value,omitempty" protobuf:"bytes,2,opt,name=value"`
	// ForceString determines whether to tell Helm to interpret booleans and numbers as strings
	ForceString bool `json:"forceString,omitempty" protobuf:"bytes,3,opt,name=forceString"`
}

// HelmFileParameter is a file parameter that's passed to helm template during manifest generation
type HelmFileParameter struct {
	// Name is the name of the Helm parameter
	Name string `json:"name,omitempty" protobuf:"bytes,1,opt,name=name"`
	// Path is the path to the file containing the values for the Helm parameter
	Path string `json:"path,omitempty" protobuf:"bytes,2,opt,name=path"`
}

var helmParameterRx = regexp.MustCompile(`([^\\]),`)

// NewHelmParameter parses a string in format name=value into a HelmParameter object and returns it
func NewHelmParameter(text string, forceString bool) (*HelmParameter, error) {
	parts := strings.SplitN(text, "=", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("Expected helm parameter of the form: param=value. Received: %s", text)
	}
	return &HelmParameter{
		Name:        parts[0],
		Value:       helmParameterRx.ReplaceAllString(parts[1], `$1\,`),
		ForceString: forceString,
	}, nil
}

// NewHelmFileParameter parses a string in format name=value into a HelmFileParameter object and returns it
func NewHelmFileParameter(text string) (*HelmFileParameter, error) {
	parts := strings.SplitN(text, "=", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("Expected helm file parameter of the form: param=path. Received: %s", text)
	}
	return &HelmFileParameter{
		Name: parts[0],
		Path: helmParameterRx.ReplaceAllString(parts[1], `$1\,`),
	}, nil
}

// AddParameter adds a HelmParameter to the application source. If a parameter with the same name already
// exists, its value will be overwritten. Otherwise, the HelmParameter will be appended as a new entry.
func (ash *ApplicationSourceHelm) AddParameter(p HelmParameter) {
	found := false
	for i, cp := range ash.Parameters {
		if cp.Name == p.Name {
			found = true
			ash.Parameters[i] = p
			break
		}
	}
	if !found {
		ash.Parameters = append(ash.Parameters, p)
	}
}

// AddFileParameter adds a HelmFileParameter to the application source. If a file parameter with the same name already
// exists, its value will be overwritten. Otherwise, the HelmFileParameter will be appended as a new entry.
func (ash *ApplicationSourceHelm) AddFileParameter(p HelmFileParameter) {
	found := false
	for i, cp := range ash.FileParameters {
		if cp.Name == p.Name {
			found = true
			ash.FileParameters[i] = p
			break
		}
	}
	if !found {
		ash.FileParameters = append(ash.FileParameters, p)
	}
}

// IsZero Returns true if the Helm options in an application source are considered zero
func (ash *ApplicationSourceHelm) IsZero() bool {
	return ash == nil || (ash.Version == "") && (ash.ReleaseName == "") && len(ash.ValueFiles) == 0 && len(ash.Parameters) == 0 && len(ash.FileParameters) == 0 && ash.ValuesIsEmpty() && !ash.PassCredentials && !ash.IgnoreMissingValueFiles && !ash.SkipCrds && !ash.SkipTests && !ash.SkipSchemaValidation && ash.KubeVersion == "" && len(ash.APIVersions) == 0 && ash.Namespace == ""
}

// KustomizeImage represents a Kustomize image definition in the format [old_image_name=]<image_name>:<image_tag>
type KustomizeImage string

func (i KustomizeImage) delim() string {
	for _, d := range []string{"=", ":", "@"} {
		if strings.Contains(string(i), d) {
			return d
		}
	}
	return ":"
}

// Match returns true if the image name matches (i.e. up to the first delimiter)
func (i KustomizeImage) Match(j KustomizeImage) bool {
	delim := j.delim()
	imageName, _, _ := strings.Cut(string(i), delim)
	otherImageName, _, _ := strings.Cut(string(j), delim)
	return imageName == otherImageName
}

// KustomizeImages is a list of Kustomize images
type KustomizeImages []KustomizeImage

// Find returns a positive integer representing the index in the list of images
func (images KustomizeImages) Find(image KustomizeImage) int {
	for i, a := range images {
		if a.Match(image) {
			return i
		}
	}
	return -1
}

// ApplicationSourceKustomize holds options specific to an Application source specific to Kustomize
type ApplicationSourceKustomize struct {
	// NamePrefix is a prefix appended to resources for Kustomize apps
	NamePrefix string `json:"namePrefix,omitempty" protobuf:"bytes,1,opt,name=namePrefix"`
	// NameSuffix is a suffix appended to resources for Kustomize apps
	NameSuffix string `json:"nameSuffix,omitempty" protobuf:"bytes,2,opt,name=nameSuffix"`
	// Images is a list of Kustomize image override specifications
	Images KustomizeImages `json:"images,omitempty" protobuf:"bytes,3,opt,name=images"`
	// CommonLabels is a list of additional labels to add to rendered manifests
	CommonLabels map[string]string `json:"commonLabels,omitempty" protobuf:"bytes,4,opt,name=commonLabels"`
	// Version controls which version of Kustomize to use for rendering manifests
	Version string `json:"version,omitempty" protobuf:"bytes,5,opt,name=version"`
	// CommonAnnotations is a list of additional annotations to add to rendered manifests
	CommonAnnotations map[string]string `json:"commonAnnotations,omitempty" protobuf:"bytes,6,opt,name=commonAnnotations"`
	// ForceCommonLabels specifies whether to force applying common labels to resources for Kustomize apps
	ForceCommonLabels bool `json:"forceCommonLabels,omitempty" protobuf:"bytes,7,opt,name=forceCommonLabels"`
	// ForceCommonAnnotations specifies whether to force applying common annotations to resources for Kustomize apps
	ForceCommonAnnotations bool `json:"forceCommonAnnotations,omitempty" protobuf:"bytes,8,opt,name=forceCommonAnnotations"`
	// Namespace sets the namespace that Kustomize adds to all resources
	Namespace string `json:"namespace,omitempty" protobuf:"bytes,9,opt,name=namespace"`
	// CommonAnnotationsEnvsubst specifies whether to apply env variables substitution for annotation values
	CommonAnnotationsEnvsubst bool `json:"commonAnnotationsEnvsubst,omitempty" protobuf:"bytes,10,opt,name=commonAnnotationsEnvsubst"`
	// Replicas is a list of Kustomize Replicas override specifications
	Replicas KustomizeReplicas `json:"replicas,omitempty" protobuf:"bytes,11,opt,name=replicas"`
	// Patches is a list of Kustomize patches
	Patches KustomizePatches `json:"patches,omitempty" protobuf:"bytes,12,opt,name=patches"`
	// Components specifies a list of kustomize components to add to the kustomization before building
	Components []string `json:"components,omitempty" protobuf:"bytes,13,rep,name=components"`
	// IgnoreMissingComponents prevents kustomize from failing when components do not exist locally by not appending them to kustomization file
	IgnoreMissingComponents bool `json:"ignoreMissingComponents,omitempty" protobuf:"bytes,17,opt,name=ignoreMissingComponents"`
	// LabelWithoutSelector specifies whether to apply common labels to resource selectors or not
	LabelWithoutSelector bool `json:"labelWithoutSelector,omitempty" protobuf:"bytes,14,opt,name=labelWithoutSelector"`
	// KubeVersion specifies the Kubernetes API version to pass to Helm when templating manifests. By default, Argo CD
	// uses the Kubernetes version of the target cluster.
	KubeVersion string `json:"kubeVersion,omitempty" protobuf:"bytes,15,opt,name=kubeVersion"`
	// APIVersions specifies the Kubernetes resource API versions to pass to Helm when templating manifests. By default,
	// Argo CD uses the API versions of the target cluster. The format is [group/]version/kind.
	APIVersions []string `json:"apiVersions,omitempty" protobuf:"bytes,16,opt,name=apiVersions"`
	// LabelIncludeTemplates specifies whether to apply common labels to resource templates or not
	LabelIncludeTemplates bool `json:"labelIncludeTemplates,omitempty" protobuf:"bytes,18,opt,name=labelIncludeTemplates"`
}

type KustomizeReplica struct {
	// Name of Deployment or StatefulSet
	Name string `json:"name" protobuf:"bytes,1,name=name"`
	// Number of replicas
	Count intstr.IntOrString `json:"count" protobuf:"bytes,2,name=count"`
}

type KustomizeReplicas []KustomizeReplica

// GetIntCount returns Count converted to int.
// If parsing error occurs, returns 0 and error.
func (kr KustomizeReplica) GetIntCount() (int, error) {
	if kr.Count.Type == intstr.String {
		count, err := strconv.Atoi(kr.Count.StrVal)
		if err != nil {
			return 0, fmt.Errorf("expected integer value for count. Received: %s", kr.Count.StrVal)
		}
		return count, nil
	}
	return kr.Count.IntValue(), nil
}

// NewKustomizeReplica parses a string in format name=count into a KustomizeReplica object and returns it
func NewKustomizeReplica(text string) (*KustomizeReplica, error) {
	parts := strings.SplitN(text, "=", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("expected parameter of the form: name=count. Received: %s", text)
	}

	kr := &KustomizeReplica{
		Name:  parts[0],
		Count: intstr.Parse(parts[1]),
	}

	if _, err := kr.GetIntCount(); err != nil {
		return nil, err
	}

	return kr, nil
}

type KustomizePatches []KustomizePatch

type KustomizePatch struct {
	Path    string             `json:"path,omitempty" yaml:"path,omitempty" protobuf:"bytes,1,opt,name=path"`
	Patch   string             `json:"patch,omitempty" yaml:"patch,omitempty" protobuf:"bytes,2,opt,name=patch"`
	Target  *KustomizeSelector `json:"target,omitempty" yaml:"target,omitempty" protobuf:"bytes,3,opt,name=target"`
	Options map[string]bool    `json:"options,omitempty" yaml:"options,omitempty" protobuf:"bytes,4,opt,name=options"`
}

// Copied from: https://github.com/kubernetes-sigs/kustomize/blob/cd7ba1744eadb793ab7cd056a76ee8a5ca725db9/api/types/patch.go
func (p *KustomizePatch) Equals(o KustomizePatch) bool {
	targetEqual := (p.Target == o.Target) ||
		(p.Target != nil && o.Target != nil && *p.Target == *o.Target)
	return p.Path == o.Path &&
		p.Patch == o.Patch &&
		targetEqual &&
		reflect.DeepEqual(p.Options, o.Options)
}

type KustomizeSelector struct {
	KustomizeResId     `json:",inline,omitempty" yaml:",inline,omitempty" protobuf:"bytes,1,opt,name=resId"`
	AnnotationSelector string `json:"annotationSelector,omitempty" yaml:"annotationSelector,omitempty" protobuf:"bytes,2,opt,name=annotationSelector"`
	LabelSelector      string `json:"labelSelector,omitempty" yaml:"labelSelector,omitempty" protobuf:"bytes,3,opt,name=labelSelector"`
}

type KustomizeResId struct {
	KustomizeGvk `json:",inline,omitempty" yaml:",inline,omitempty" protobuf:"bytes,1,opt,name=gvk"`
	Name         string `json:"name,omitempty" yaml:"name,omitempty" protobuf:"bytes,2,opt,name=name"`
	Namespace    string `json:"namespace,omitempty" yaml:"namespace,omitempty" protobuf:"bytes,3,opt,name=namespace"`
}

type KustomizeGvk struct {
	Group   string `json:"group,omitempty" yaml:"group,omitempty" protobuf:"bytes,1,opt,name=group"`
	Version string `json:"version,omitempty" yaml:"version,omitempty" protobuf:"bytes,2,opt,name=version"`
	Kind    string `json:"kind,omitempty" yaml:"kind,omitempty" protobuf:"bytes,3,opt,name=kind"`
}

// AllowsConcurrentProcessing returns true if multiple processes can run Kustomize builds on the same source at the same time
func (k *ApplicationSourceKustomize) AllowsConcurrentProcessing() bool {
	return len(k.Images) == 0 &&
		len(k.CommonLabels) == 0 &&
		len(k.CommonAnnotations) == 0 &&
		k.NamePrefix == "" &&
		k.Namespace == "" &&
		k.NameSuffix == "" &&
		len(k.Patches) == 0 &&
		len(k.Components) == 0
}

// IsZero returns true when the Kustomize options are considered empty
func (k *ApplicationSourceKustomize) IsZero() bool {
	return k == nil ||
		k.NamePrefix == "" &&
			k.NameSuffix == "" &&
			k.Version == "" &&
			k.Namespace == "" &&
			len(k.Images) == 0 &&
			len(k.Replicas) == 0 &&
			len(k.CommonLabels) == 0 &&
			len(k.CommonAnnotations) == 0 &&
			len(k.Patches) == 0 &&
			len(k.Components) == 0 &&
			k.KubeVersion == "" &&
			len(k.APIVersions) == 0 &&
			!k.IgnoreMissingComponents
}

// MergeImage merges a new Kustomize image identifier in to a list of images
func (k *ApplicationSourceKustomize) MergeImage(image KustomizeImage) {
	i := k.Images.Find(image)
	if i >= 0 {
		k.Images[i] = image
	} else {
		k.Images = append(k.Images, image)
	}
}

// MergeReplica merges a new Kustomize replica identifier in to a list of replicas
func (k *ApplicationSourceKustomize) MergeReplica(replica KustomizeReplica) {
	i := k.Replicas.FindByName(replica.Name)
	if i >= 0 {
		k.Replicas[i] = replica
	} else {
		k.Replicas = append(k.Replicas, replica)
	}
}

// Find returns a positive integer representing the index in the list of replicas
func (rs KustomizeReplicas) FindByName(name string) int {
	for i, r := range rs {
		if r.Name == name {
			return i
		}
	}
	return -1
}

// JsonnetVar represents a variable to be passed to jsonnet during manifest generation
type JsonnetVar struct {
	Name  string `json:"name" protobuf:"bytes,1,opt,name=name"`
	Value string `json:"value" protobuf:"bytes,2,opt,name=value"`
	Code  bool   `json:"code,omitempty" protobuf:"bytes,3,opt,name=code"`
}

// NewJsonnetVar parses a Jsonnet variable from a string in the format name=value
func NewJsonnetVar(s string, code bool) JsonnetVar {
	parts := strings.SplitN(s, "=", 2)
	if len(parts) == 2 {
		return JsonnetVar{Name: parts[0], Value: parts[1], Code: code}
	}
	return JsonnetVar{Name: s, Code: code}
}

// ApplicationSourceJsonnet holds options specific to applications of type Jsonnet
type ApplicationSourceJsonnet struct {
	// ExtVars is a list of Jsonnet External Variables
	ExtVars []JsonnetVar `json:"extVars,omitempty" protobuf:"bytes,1,opt,name=extVars"`
	// TLAS is a list of Jsonnet Top-level Arguments
	TLAs []JsonnetVar `json:"tlas,omitempty" protobuf:"bytes,2,opt,name=tlas"`
	// Additional library search dirs
	Libs []string `json:"libs,omitempty" protobuf:"bytes,3,opt,name=libs"`
}

// IsZero returns true if the JSonnet options of an application are considered to be empty
func (j *ApplicationSourceJsonnet) IsZero() bool {
	return j == nil || len(j.ExtVars) == 0 && len(j.TLAs) == 0 && len(j.Libs) == 0
}

// ApplicationSourceDirectory holds options for applications of type plain YAML or Jsonnet
type ApplicationSourceDirectory struct {
	// Recurse specifies whether to scan a directory recursively for manifests
	Recurse bool `json:"recurse,omitempty" protobuf:"bytes,1,opt,name=recurse"`
	// Jsonnet holds options specific to Jsonnet
	Jsonnet ApplicationSourceJsonnet `json:"jsonnet,omitempty" protobuf:"bytes,2,opt,name=jsonnet"`
	// Exclude contains a glob pattern to match paths against that should be explicitly excluded from being used during manifest generation
	Exclude string `json:"exclude,omitempty" protobuf:"bytes,3,opt,name=exclude"`
	// Include contains a glob pattern to match paths against that should be explicitly included during manifest generation
	Include string `json:"include,omitempty" protobuf:"bytes,4,opt,name=include"`
}

// IsZero returns true if the ApplicationSourceDirectory is considered empty
func (d *ApplicationSourceDirectory) IsZero() bool {
	return d == nil || !d.Recurse && d.Jsonnet.IsZero()
}

type OptionalMap struct {
	// Map is the value of a map type parameter.
	// +optional
	Map map[string]string `json:"map" protobuf:"bytes,1,rep,name=map"`
	// We need the explicit +optional so that kube-builder generates the CRD without marking this as required.
}

// Equals returns true if the two OptionalMap objects are equal. We can't use reflect.DeepEqual because it will return
// false if one of the maps is nil and the other is an empty map. This is because the JSON unmarshaller will set the
// map to nil if it is empty, but the protobuf unmarshaller will set it to an empty map.
func (o *OptionalMap) Equals(other *OptionalMap) bool {
	if o == nil && other == nil {
		return true
	}
	if o == nil || other == nil {
		return false
	}
	if len(o.Map) != len(other.Map) {
		return false
	}
	if o.Map == nil && other.Map == nil {
		return true
	}
	// The next two blocks are critical. Depending on whether the struct was populated from JSON or protobufs, the map
	// field will be either nil or an empty map. They mean the same thing: the map is empty.
	if o.Map == nil && len(other.Map) == 0 {
		return true
	}
	if other.Map == nil && len(o.Map) == 0 {
		return true
	}
	return reflect.DeepEqual(o.Map, other.Map)
}

type OptionalArray struct {
	// Array is the value of an array type parameter.
	// +optional
	Array []string `json:"array" protobuf:"bytes,1,rep,name=array"`
	// We need the explicit +optional so that kube-builder generates the CRD without marking this as required.
}

// Equals returns true if the two OptionalArray objects are equal. We can't use reflect.DeepEqual because it will return
// false if one of the arrays is nil and the other is an empty array. This is because the JSON unmarshaller will set the
// array to nil if it is empty, but the protobuf unmarshaller will set it to an empty array.
func (o *OptionalArray) Equals(other *OptionalArray) bool {
	if o == nil && other == nil {
		return true
	}
	if o == nil || other == nil {
		return false
	}
	if len(o.Array) != len(other.Array) {
		return false
	}
	if o.Array == nil && other.Array == nil {
		return true
	}
	// The next two blocks are critical. Depending on whether the struct was populated from JSON or protobufs, the array
	// field will be either nil or an empty array. They mean the same thing: the array is empty.
	if o.Array == nil && len(other.Array) == 0 {
		return true
	}
	if other.Array == nil && len(o.Array) == 0 {
		return true
	}
	return reflect.DeepEqual(o.Array, other.Array)
}

type ApplicationSourcePluginParameter struct {
	// We use pointers to structs because go-to-protobuf represents pointers to arrays/maps as repeated fields.
	// These repeated fields have no way to represent "present but empty." So we would have no way to distinguish
	// {name: parameters, array: []} from {name: parameter}
	// By wrapping the array/map in a struct, we can use a pointer to the struct to represent "present but empty."

	// Name is the name identifying a parameter.
	Name string `json:"name,omitempty" protobuf:"bytes,1,opt,name=name"`
	// String_ is the value of a string type parameter.
	String_ *string `json:"string,omitempty" protobuf:"bytes,5,opt,name=string"` //nolint:revive //FIXME(var-naming)
	// Map is the value of a map type parameter.
	*OptionalMap `json:",omitempty" protobuf:"bytes,3,rep,name=map"`
	// Array is the value of an array type parameter.
	*OptionalArray `json:",omitempty" protobuf:"bytes,4,rep,name=array"`
}

func (p ApplicationSourcePluginParameter) Equals(other ApplicationSourcePluginParameter) bool {
	if p.Name != other.Name {
		return false
	}
	if !reflect.DeepEqual(p.String_, other.String_) {
		return false
	}
	return p.OptionalMap.Equals(other.OptionalMap) && p.OptionalArray.Equals(other.OptionalArray)
}

// MarshalJSON is a custom JSON marshaller for ApplicationSourcePluginParameter. We need this custom marshaler because,
// when ApplicationSourcePluginParameter is unmarshaled, either from JSON or protobufs, the fields inside OptionalMap and
// OptionalArray are not set. The default JSON marshaler marshals these as "null." But really what we want to represent
// is an empty map or array.
//
// There are efforts to change things upstream, but nothing has been merged yet. See https://github.com/golang/go/issues/37711
func (p ApplicationSourcePluginParameter) MarshalJSON() ([]byte, error) {
	out := map[string]any{}
	out["name"] = p.Name
	if p.String_ != nil {
		out["string"] = p.String_
	}
	if p.OptionalMap != nil {
		if p.OptionalMap.Map == nil {
			// Nil is not the same as a nil map. Nil means the field was not set, while a nil map means the field was set to an empty map.
			// Either way, we want to marshal it as "{}".
			out["map"] = map[string]string{}
		} else {
			out["map"] = p.OptionalMap.Map
		}
	}
	if p.OptionalArray != nil {
		if p.OptionalArray.Array == nil {
			// Nil is not the same as a nil array. Nil means the field was not set, while a nil array means the field was set to an empty array.
			// Either way, we want to marshal it as "[]".
			out["array"] = []string{}
		} else {
			out["array"] = p.OptionalArray.Array
		}
	}
	bytes, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}
	return bytes, nil
}

type ApplicationSourcePluginParameters []ApplicationSourcePluginParameter

func (p ApplicationSourcePluginParameters) Equals(other ApplicationSourcePluginParameters) bool {
	if len(p) != len(other) {
		return false
	}
	for i := range p {
		if !p[i].Equals(other[i]) {
			return false
		}
	}
	return true
}

func (p ApplicationSourcePluginParameters) IsZero() bool {
	return len(p) == 0
}

// Environ builds a list of environment variables to represent parameters sent to a plugin from the Application
// manifest. Parameters are represented as one large stringified JSON array (under `ARGOCD_APP_PARAMETERS`). They're
// also represented as individual environment variables, each variable's key being an escaped version of the parameter's
// name.
func (p ApplicationSourcePluginParameters) Environ() ([]string, error) {
	out, err := json.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal plugin parameters: %w", err)
	}
	jsonParam := "ARGOCD_APP_PARAMETERS=" + string(out)

	env := []string{jsonParam}

	for _, param := range p {
		envBaseName := "PARAM_" + escaped(param.Name)
		if param.String_ != nil {
			env = append(env, fmt.Sprintf("%s=%s", envBaseName, *param.String_))
		}
		if param.OptionalMap != nil {
			for key, value := range param.OptionalMap.Map {
				env = append(env, fmt.Sprintf("%s_%s=%s", envBaseName, escaped(key), value))
			}
		}
		if param.OptionalArray != nil {
			for i, value := range param.OptionalArray.Array {
				env = append(env, fmt.Sprintf("%s_%d=%s", envBaseName, i, value))
			}
		}
	}

	return env, nil
}

func escaped(paramName string) string {
	newParamName := strings.ToUpper(paramName)
	invalidParamCharRegex := regexp.MustCompile("[^A-Z0-9_]")
	return invalidParamCharRegex.ReplaceAllString(newParamName, "_")
}

// ApplicationSourcePlugin holds options specific to config management plugins
type ApplicationSourcePlugin struct {
	Name       string `json:"name,omitempty" protobuf:"bytes,1,opt,name=name"`
	Env        `json:"env,omitempty" protobuf:"bytes,2,opt,name=env"`
	Parameters ApplicationSourcePluginParameters `json:"parameters,omitempty" protobuf:"bytes,3,opt,name=parameters"`
}

func (c *ApplicationSourcePlugin) Equals(other *ApplicationSourcePlugin) bool {
	if c == nil && other == nil {
		return true
	}
	if c == nil || other == nil {
		return false
	}
	if !c.Parameters.Equals(other.Parameters) {
		return false
	}
	// DeepEqual works fine for fields besides Parameters. Since we already know that Parameters are equal, we can
	// set them to nil and then do a DeepEqual.
	leftCopy := c.DeepCopy()
	rightCopy := other.DeepCopy()
	leftCopy.Parameters = nil
	rightCopy.Parameters = nil
	return reflect.DeepEqual(leftCopy, rightCopy)
}

// IsZero returns true if the ApplicationSourcePlugin is considered empty
func (c *ApplicationSourcePlugin) IsZero() bool {
	return c == nil || c.Name == "" && c.Env.IsZero() && c.Parameters.IsZero()
}

// AddEnvEntry merges an EnvEntry into a list of entries. If an entry with the same name already exists,
// its value will be overwritten. Otherwise, the entry is appended to the list.
func (c *ApplicationSourcePlugin) AddEnvEntry(e *EnvEntry) {
	found := false
	for i, ce := range c.Env {
		if ce.Name == e.Name {
			found = true
			c.Env[i] = e
			break
		}
	}
	if !found {
		c.Env = append(c.Env, e)
	}
}

// RemoveEnvEntry removes an EnvEntry if present, from a list of entries.
func (c *ApplicationSourcePlugin) RemoveEnvEntry(key string) error {
	for i, ce := range c.Env {
		if ce.Name == key {
			c.Env[i] = c.Env[len(c.Env)-1]
			c.Env = c.Env[:len(c.Env)-1]
			return nil
		}
	}
	return fmt.Errorf("unable to find env variable with key %q for plugin %q", key, c.Name)
}

// ApplicationDestination holds information about the application's destination
type ApplicationDestination struct {
	// Server specifies the URL of the target cluster's Kubernetes control plane API. This must be set if Name is not set.
	Server string `json:"server,omitempty" protobuf:"bytes,1,opt,name=server"`
	// Namespace specifies the target namespace for the application's resources.
	// The namespace will only be set for namespace-scoped resources that have not set a value for .metadata.namespace
	Namespace string `json:"namespace,omitempty" protobuf:"bytes,2,opt,name=namespace"`
	// Name is an alternate way of specifying the target cluster by its symbolic name. This must be set if Server is not set.
	Name string `json:"name,omitempty" protobuf:"bytes,3,opt,name=name"`
}

type ResourceHealthLocation string

var (
	ResourceHealthLocationInline  ResourceHealthLocation
	ResourceHealthLocationAppTree ResourceHealthLocation = "appTree"
)

const (
	APIVersion  = "argoproj.io/v1alpha1"
	KindCluster = "Application"
)

// Custom Marshalling Logic so that users do not need to explicity fill out the Kind and ApiVersion.
func (application Application) MarshalJSON() ([]byte, error) {
	application.Kind = KindCluster
	application.APIVersion = APIVersion

	type applicationAlt Application
	return json.Marshal(applicationAlt(application))
}

// Custom Unmarshalling to raise an error if the ApiVersion or Kind does not match.
func (application *Application) UnmarshalJSON(data []byte) error {
	type ApplicationAlt Application
	if err := json.Unmarshal(data, (*ApplicationAlt)(application)); err != nil {
		return err
	}
	if application.APIVersion != APIVersion {
		return fmt.Errorf("unexpected api version: expected %s but got %s", APIVersion, application.APIVersion)
	}
	if application.Kind != KindCluster {
		return fmt.Errorf("unexpected kind: expected %s but got %s", KindCluster, application.Kind)
	}
	return nil
}

// ApplicationStatus contains status information for the application
type ApplicationStatus struct {
	// Resources is a list of Kubernetes resources managed by this application
	Resources []ResourceStatus `json:"resources,omitempty" protobuf:"bytes,1,opt,name=resources"`
	// Sync contains information about the application's current sync status
	Sync SyncStatus `json:"sync,omitempty" protobuf:"bytes,2,opt,name=sync"`
	// Health contains information about the application's current health status
	Health HealthStatus `json:"health,omitempty" protobuf:"bytes,3,opt,name=health"`
	// History contains information about the application's sync history
	History RevisionHistories `json:"history,omitempty" protobuf:"bytes,4,opt,name=history"`
	// Conditions is a list of currently observed application conditions
	Conditions []ApplicationCondition `json:"conditions,omitempty" protobuf:"bytes,5,opt,name=conditions"`
	// ReconciledAt indicates when the application state was reconciled using the latest git version
	ReconciledAt *metav1.Time `json:"reconciledAt,omitempty" protobuf:"bytes,6,opt,name=reconciledAt"`
	// OperationState contains information about any ongoing operations, such as a sync
	OperationState *OperationState `json:"operationState,omitempty" protobuf:"bytes,7,opt,name=operationState"`
	// ObservedAt indicates when the application state was updated without querying latest git state
	// Deprecated: controller no longer updates ObservedAt field
	ObservedAt *metav1.Time `json:"observedAt,omitempty" protobuf:"bytes,8,opt,name=observedAt"`
	// SourceType specifies the type of this application
	SourceType ApplicationSourceType `json:"sourceType,omitempty" protobuf:"bytes,9,opt,name=sourceType"`
	// Summary contains a list of URLs and container images used by this application
	Summary ApplicationSummary `json:"summary,omitempty" protobuf:"bytes,10,opt,name=summary"`
	// ResourceHealthSource indicates where the resource health status is stored: inline if not set or appTree
	ResourceHealthSource ResourceHealthLocation `json:"resourceHealthSource,omitempty" protobuf:"bytes,11,opt,name=resourceHealthSource"`
	// SourceTypes specifies the type of the sources included in the application
	SourceTypes []ApplicationSourceType `json:"sourceTypes,omitempty" protobuf:"bytes,12,opt,name=sourceTypes"`
	// ControllerNamespace indicates the namespace in which the application controller is located
	ControllerNamespace string `json:"controllerNamespace,omitempty" protobuf:"bytes,13,opt,name=controllerNamespace"`
}

func (status *ApplicationStatus) FindResource(key ResourceKey) (*ResourceStatus, bool) {
	for i := range status.Resources {
		res := status.Resources[i]
		if NewResourceKey(res.Group, res.Kind, res.Namespace, res.Name) == key {
			return &res, true
		}
	}
	return nil, false
}

// GetRevisions will return the current revision associated with the Application.
// If app has multisources, it will return all corresponding revisions preserving
// order from the app.spec.sources. If app has only one source, it will return a
// single revision in the list.
func (status *ApplicationStatus) GetRevisions() []string {
	revisions := []string{}
	if len(status.Sync.Revisions) > 0 {
		revisions = status.Sync.Revisions
	} else if status.Sync.Revision != "" {
		revisions = append(revisions, status.Sync.Revision)
	}
	return revisions
}

// BuildComparedToStatus will build a ComparedTo object based on the current
// Application state.
func (spec *ApplicationSpec) BuildComparedToStatus() ComparedTo {
	ct := ComparedTo{
		Destination:       spec.Destination,
		IgnoreDifferences: spec.IgnoreDifferences,
	}
	if spec.HasMultipleSources() {
		ct.Sources = spec.Sources
	} else {
		ct.Source = spec.GetSource()
	}
	return ct
}

// JWTTokens represents a list of JWT tokens
type JWTTokens struct {
	Items []JWTToken `json:"items,omitempty" protobuf:"bytes,1,opt,name=items"`
}

// OperationInitiator contains information about the initiator of an operation
type OperationInitiator struct {
	// Username contains the name of a user who started operation
	Username string `json:"username,omitempty" protobuf:"bytes,1,opt,name=username"`
	// Automated is set to true if operation was initiated automatically by the application controller.
	Automated bool `json:"automated,omitempty" protobuf:"bytes,2,opt,name=automated"`
}

// Operation contains information about a requested or running operation
type Operation struct {
	// Sync contains parameters for the operation
	Sync *SyncOperation `json:"sync,omitempty" protobuf:"bytes,1,opt,name=sync"`
	// InitiatedBy contains information about who initiated the operations
	InitiatedBy OperationInitiator `json:"initiatedBy,omitempty" protobuf:"bytes,2,opt,name=initiatedBy"`
	// Info is a list of informational items for this operation
	Info []*Info `json:"info,omitempty" protobuf:"bytes,3,name=info"`
	// Retry controls the strategy to apply if a sync fails
	Retry RetryStrategy `json:"retry,omitempty" protobuf:"bytes,4,opt,name=retry"`
}

// DryRun returns true if an operation was requested to be performed in dry run mode
func (o *Operation) DryRun() bool {
	if o.Sync != nil {
		return o.Sync.DryRun
	}
	return false
}

// SyncOperationResource contains resources to sync.
type SyncOperationResource struct {
	Group     string `json:"group,omitempty" protobuf:"bytes,1,opt,name=group"`
	Kind      string `json:"kind" protobuf:"bytes,2,opt,name=kind"`
	Name      string `json:"name" protobuf:"bytes,3,opt,name=name"`
	Namespace string `json:"namespace,omitempty" protobuf:"bytes,4,opt,name=namespace"`
	Exclude   bool   `json:"-"`
}

// RevisionHistories is a array of history, oldest first and newest last
type RevisionHistories []RevisionHistory

// LastRevisionHistory returns the latest history item from the revision history
func (in RevisionHistories) LastRevisionHistory() RevisionHistory {
	return in[len(in)-1]
}

// Trunc truncates the list of history items to size n
func (in RevisionHistories) Trunc(n int) RevisionHistories {
	i := len(in) - n
	if i > 0 {
		in = in[i:]
	}
	return in
}

// HasIdentity determines whether a sync operation is identified by a manifest
func (r SyncOperationResource) HasIdentity(name string, namespace string, gvk schema.GroupVersionKind) bool {
	if name == r.Name && gvk.Kind == r.Kind && gvk.Group == r.Group && (r.Namespace == "" || namespace == r.Namespace) {
		return true
	}
	return false
}

// Compare determines whether an app resource matches the resource filter during sync or wait.
func (r SyncOperationResource) Compare(name string, namespace string, gvk schema.GroupVersionKind) bool {
	if (r.Group == "*" || gvk.Group == r.Group) &&
		(r.Kind == "*" || gvk.Kind == r.Kind) &&
		(r.Name == "*" || name == r.Name) &&
		(r.Namespace == "*" || r.Namespace == "" || namespace == r.Namespace) {
		return true
	}
	return false
}

// SyncOperation contains details about a sync operation.
type SyncOperation struct {
	// Revision is the revision (Git) or chart version (Helm) which to sync the application to
	// If omitted, will use the revision specified in app spec.
	Revision string `json:"revision,omitempty" protobuf:"bytes,1,opt,name=revision"`
	// Prune specifies to delete resources from the cluster that are no longer tracked in git
	Prune bool `json:"prune,omitempty" protobuf:"bytes,2,opt,name=prune"`
	// DryRun specifies to perform a `kubectl apply --dry-run` without actually performing the sync
	DryRun bool `json:"dryRun,omitempty" protobuf:"bytes,3,opt,name=dryRun"`
	// SyncStrategy describes how to perform the sync
	SyncStrategy *SyncStrategy `json:"syncStrategy,omitempty" protobuf:"bytes,4,opt,name=syncStrategy"`
	// Resources describes which resources shall be part of the sync
	Resources []SyncOperationResource `json:"resources,omitempty" protobuf:"bytes,6,opt,name=resources"`
	// Source overrides the source definition set in the application.
	// This is typically set in a Rollback operation and is nil during a Sync operation
	Source *ApplicationSource `json:"source,omitempty" protobuf:"bytes,7,opt,name=source"`
	// Manifests is an optional field that overrides sync source with a local directory for development
	Manifests []string `json:"manifests,omitempty" protobuf:"bytes,8,opt,name=manifests"`
	// SyncOptions provide per-sync sync-options, e.g. Validate=false
	SyncOptions SyncOptions `json:"syncOptions,omitempty" protobuf:"bytes,9,opt,name=syncOptions"`
	// Sources overrides the source definition set in the application.
	// This is typically set in a Rollback operation and is nil during a Sync operation
	Sources ApplicationSources `json:"sources,omitempty" protobuf:"bytes,10,opt,name=sources"`
	// Revisions is the list of revision (Git) or chart version (Helm) which to sync each source in sources field for the application to
	// If omitted, will use the revision specified in app spec.
	Revisions []string `json:"revisions,omitempty" protobuf:"bytes,11,opt,name=revisions"`
	// SelfHealAttemptsCount contains the number of auto-heal attempts
	SelfHealAttemptsCount int64 `json:"autoHealAttemptsCount,omitempty" protobuf:"bytes,12,opt,name=autoHealAttemptsCount"`
}

// IsApplyStrategy returns true if the sync strategy is "apply"
func (o *SyncOperation) IsApplyStrategy() bool {
	return o.SyncStrategy != nil && o.SyncStrategy.Apply != nil
}

// OperationState contains information about state of a running operation
type OperationState struct {
	// Operation is the original requested operation
	Operation Operation `json:"operation" protobuf:"bytes,1,opt,name=operation"`
	// Phase is the current phase of the operation
	Phase OperationPhase `json:"phase" protobuf:"bytes,2,opt,name=phase"`
	// Message holds any pertinent messages when attempting to perform operation (typically errors).
	Message string `json:"message,omitempty" protobuf:"bytes,3,opt,name=message"`
	// SyncResult is the result of a Sync operation
	SyncResult *SyncOperationResult `json:"syncResult,omitempty" protobuf:"bytes,4,opt,name=syncResult"`
	// StartedAt contains time of operation start
	StartedAt metav1.Time `json:"startedAt" protobuf:"bytes,6,opt,name=startedAt"`
	// FinishedAt contains time of operation completion
	FinishedAt *metav1.Time `json:"finishedAt,omitempty" protobuf:"bytes,7,opt,name=finishedAt"`
	// RetryCount contains time of operation retries
	RetryCount int64 `json:"retryCount,omitempty" protobuf:"bytes,8,opt,name=retryCount"`
}

type Info struct {
	Name  string `json:"name" protobuf:"bytes,1,name=name"`
	Value string `json:"value" protobuf:"bytes,2,name=value"`
}

type SyncOptions []string

// AddOption adds a sync option to the list of sync options and returns the modified list.
// If option was already set, returns the unmodified list of sync options.
func (o SyncOptions) AddOption(option string) SyncOptions {
	for _, j := range o {
		if j == option {
			return o
		}
	}
	return append(o, option)
}

// RemoveOption removes a sync option from the list of sync options and returns the modified list.
// If option has not been already set, returns the unmodified list of sync options.
func (o SyncOptions) RemoveOption(option string) SyncOptions {
	for i, j := range o {
		if j == option {
			return append(o[:i], o[i+1:]...)
		}
	}
	return o
}

// HasOption returns true if the list of sync options contains given option
func (o SyncOptions) HasOption(option string) bool {
	for _, i := range o {
		if option == i {
			return true
		}
	}
	return false
}

type ManagedNamespaceMetadata struct {
	Labels      map[string]string `json:"labels,omitempty" protobuf:"bytes,1,opt,name=labels"`
	Annotations map[string]string `json:"annotations,omitempty" protobuf:"bytes,2,opt,name=annotations"`
}

// SyncPolicy controls when a sync will be performed in response to updates in git
type SyncPolicy struct {
	// Automated will keep an application synced to the target revision
	Automated *SyncPolicyAutomated `json:"automated,omitempty" protobuf:"bytes,1,opt,name=automated"`
	// Options allow you to specify whole app sync-options
	SyncOptions SyncOptions `json:"syncOptions,omitempty" protobuf:"bytes,2,opt,name=syncOptions"`
	// Retry controls failed sync retry behavior
	Retry *RetryStrategy `json:"retry,omitempty" protobuf:"bytes,3,opt,name=retry"`
	// ManagedNamespaceMetadata controls metadata in the given namespace (if CreateNamespace=true)
	ManagedNamespaceMetadata *ManagedNamespaceMetadata `json:"managedNamespaceMetadata,omitempty" protobuf:"bytes,4,opt,name=managedNamespaceMetadata"`
	// If you add a field here, be sure to update IsZero.
}

// IsZero returns true if the sync policy is empty
func (p *SyncPolicy) IsZero() bool {
	return p == nil || (p.Automated == nil && len(p.SyncOptions) == 0 && p.Retry == nil && p.ManagedNamespaceMetadata == nil)
}

// RetryStrategy contains information about the strategy to apply when a sync failed
type RetryStrategy struct {
	// Limit is the maximum number of attempts for retrying a failed sync. If set to 0, no retries will be performed.
	Limit int64 `json:"limit,omitempty" protobuf:"bytes,1,opt,name=limit"`
	// Backoff controls how to backoff on subsequent retries of failed syncs
	Backoff *Backoff `json:"backoff,omitempty" protobuf:"bytes,2,opt,name=backoff,casttype=Backoff"`
}

func parseStringToDuration(durationString string) (time.Duration, error) {
	var suspendDuration time.Duration
	// If no units are attached, treat as seconds
	if val, err := strconv.Atoi(durationString); err == nil {
		suspendDuration = time.Duration(val) * time.Second
	} else if duration, err := time.ParseDuration(durationString); err == nil {
		suspendDuration = duration
	} else {
		return 0, fmt.Errorf("unable to parse %s as a duration", durationString)
	}
	return suspendDuration, nil
}

// NextRetryAt calculates the earliest time the next retry should be performed on a failing sync
func (r *RetryStrategy) NextRetryAt(lastAttempt time.Time, retryCounts int64) (time.Time, error) {
	maxDuration := DefaultSyncRetryMaxDuration
	duration := DefaultSyncRetryDuration
	factor := DefaultSyncRetryFactor
	var err error
	if r.Backoff != nil {
		if r.Backoff.Duration != "" {
			if duration, err = parseStringToDuration(r.Backoff.Duration); err != nil {
				return time.Time{}, err
			}
		}
		if r.Backoff.MaxDuration != "" {
			if maxDuration, err = parseStringToDuration(r.Backoff.MaxDuration); err != nil {
				return time.Time{}, err
			}
		}
		if r.Backoff.Factor != nil {
			factor = *r.Backoff.Factor
		}
	}
	// Formula: timeToWait = duration * factor^retry_number
	// Note that timeToWait should equal to duration for the first retry attempt.
	// When timeToWait is more than maxDuration retry should be performed at maxDuration.
	timeToWait := float64(duration) * (math.Pow(float64(factor), float64(retryCounts)))
	if maxDuration > 0 {
		timeToWait = math.Min(float64(maxDuration), timeToWait)
	}
	return lastAttempt.Add(time.Duration(timeToWait)), nil
}

// Backoff is the backoff strategy to use on subsequent retries for failing syncs
type Backoff struct {
	// Duration is the amount to back off. Default unit is seconds, but could also be a duration (e.g. "2m", "1h")
	Duration string `json:"duration,omitempty" protobuf:"bytes,1,opt,name=duration"`
	// Factor is a factor to multiply the base duration after each failed retry
	Factor *int64 `json:"factor,omitempty" protobuf:"bytes,2,name=factor"`
	// MaxDuration is the maximum amount of time allowed for the backoff strategy
	MaxDuration string `json:"maxDuration,omitempty" protobuf:"bytes,3,opt,name=maxDuration"`
}

// SyncPolicyAutomated controls the behavior of an automated sync
type SyncPolicyAutomated struct {
	// Prune specifies whether to delete resources from the cluster that are not found in the sources anymore as part of automated sync (default: false)
	Prune bool `json:"prune,omitempty" protobuf:"bytes,1,opt,name=prune"`
	// SelfHeal specifies whether to revert resources back to their desired state upon modification in the cluster (default: false)
	SelfHeal bool `json:"selfHeal,omitempty" protobuf:"bytes,2,opt,name=selfHeal"`
	// AllowEmpty allows apps have zero live resources (default: false)
	AllowEmpty bool `json:"allowEmpty,omitempty" protobuf:"bytes,3,opt,name=allowEmpty"`
}

// SyncStrategy controls the manner in which a sync is performed
type SyncStrategy struct {
	// Apply will perform a `kubectl apply` to perform the sync.
	Apply *SyncStrategyApply `json:"apply,omitempty" protobuf:"bytes,1,opt,name=apply"`
	// Hook will submit any referenced resources to perform the sync. This is the default strategy
	Hook *SyncStrategyHook `json:"hook,omitempty" protobuf:"bytes,2,opt,name=hook"`
}

// Force returns true if the sync strategy specifies to perform a forced sync
func (m *SyncStrategy) Force() bool {
	switch {
	case m == nil:
		return false
	case m.Apply != nil:
		return m.Apply.Force
	case m.Hook != nil:
		return m.Hook.Force
	}
	return false
}

// SyncStrategyApply uses `kubectl apply` to perform the apply
type SyncStrategyApply struct {
	// Force indicates whether or not to supply the --force flag to `kubectl apply`.
	// The --force flag deletes and re-create the resource, when PATCH encounters conflict and has
	// retried for 5 times.
	Force bool `json:"force,omitempty" protobuf:"bytes,1,opt,name=force"`
}

// SyncStrategyHook will perform a sync using hooks annotations.
// If no hook annotation is specified falls back to `kubectl apply`.
type SyncStrategyHook struct {
	// Embed SyncStrategyApply type to inherit any `apply` options
	// +optional
	SyncStrategyApply `json:",inline" protobuf:"bytes,1,opt,name=syncStrategyApply"`
}

// RevisionMetadata contains metadata for a specific revision in a Git repository
type RevisionMetadata struct {
	// who authored this revision,
	// typically their name and email, e.g. "John Doe <john_doe@my-company.com>",
	// but might not match this example
	Author string `json:"author,omitempty" protobuf:"bytes,1,opt,name=author"`
	// Date specifies when the revision was authored
	Date metav1.Time `json:"date" protobuf:"bytes,2,opt,name=date"`
	// Tags specifies any tags currently attached to the revision
	// Floating tags can move from one revision to another
	Tags []string `json:"tags,omitempty" protobuf:"bytes,3,opt,name=tags"`
	// Message contains the message associated with the revision, most likely the commit message.
	Message string `json:"message,omitempty" protobuf:"bytes,4,opt,name=message"`
	// SignatureInfo contains a hint on the signer if the revision was signed with GPG, and signature verification is enabled.
	SignatureInfo string `json:"signatureInfo,omitempty" protobuf:"bytes,5,opt,name=signatureInfo"`
}

// ChartDetails contains helm chart metadata for a specific version
type ChartDetails struct {
	Description string `json:"description,omitempty" protobuf:"bytes,1,opt,name=description"`
	// The URL of this projects home page, e.g. "http://example.com"
	Home string `json:"home,omitempty" protobuf:"bytes,2,opt,name=home"`
	// List of maintainer details, name and email, e.g. ["John Doe <john_doe@my-company.com>"]
	Maintainers []string `json:"maintainers,omitempty" protobuf:"bytes,3,opt,name=maintainers"`
}

// SyncOperationResult represent result of sync operation
type SyncOperationResult struct {
	// Resources contains a list of sync result items for each individual resource in a sync operation
	Resources ResourceResults `json:"resources,omitempty" protobuf:"bytes,1,opt,name=resources"`
	// Revision holds the revision this sync operation was performed to
	Revision string `json:"revision" protobuf:"bytes,2,opt,name=revision"`
	// Source records the application source information of the sync, used for comparing auto-sync
	Source ApplicationSource `json:"source,omitempty" protobuf:"bytes,3,opt,name=source"`
	// Source records the application source information of the sync, used for comparing auto-sync
	Sources ApplicationSources `json:"sources,omitempty" protobuf:"bytes,4,opt,name=sources"`
	// Revisions holds the revision this sync operation was performed for respective indexed source in sources field
	Revisions []string `json:"revisions,omitempty" protobuf:"bytes,5,opt,name=revisions"`
	// ManagedNamespaceMetadata contains the current sync state of managed namespace metadata
	ManagedNamespaceMetadata *ManagedNamespaceMetadata `json:"managedNamespaceMetadata,omitempty" protobuf:"bytes,6,opt,name=managedNamespaceMetadata"`
}

// ResourceResult holds the operation result details of a specific resource
type ResourceResult struct {
	// Group specifies the API group of the resource
	Group string `json:"group" protobuf:"bytes,1,opt,name=group"`
	// Version specifies the API version of the resource
	Version string `json:"version" protobuf:"bytes,2,opt,name=version"`
	// Kind specifies the API kind of the resource
	Kind string `json:"kind" protobuf:"bytes,3,opt,name=kind"`
	// Namespace specifies the target namespace of the resource
	Namespace string `json:"namespace" protobuf:"bytes,4,opt,name=namespace"`
	// Name specifies the name of the resource
	Name string `json:"name" protobuf:"bytes,5,opt,name=name"`
	// Status holds the final result of the sync. Will be empty if the resources is yet to be applied/pruned and is always zero-value for hooks
	Status ResultCode `json:"status,omitempty" protobuf:"bytes,6,opt,name=status"`
	// Message contains an informational or error message for the last sync OR operation
	Message string `json:"message,omitempty" protobuf:"bytes,7,opt,name=message"`
	// HookType specifies the type of the hook. Empty for non-hook resources
	HookType HookType `json:"hookType,omitempty" protobuf:"bytes,8,opt,name=hookType"`
	// HookPhase contains the state of any operation associated with this resource OR hook
	// This can also contain values for non-hook resources.
	HookPhase OperationPhase `json:"hookPhase,omitempty" protobuf:"bytes,9,opt,name=hookPhase"`
	// SyncPhase indicates the particular phase of the sync that this result was acquired in
	SyncPhase SyncPhase `json:"syncPhase,omitempty" protobuf:"bytes,10,opt,name=syncPhase"`
}

type SyncPhase string

// SyncWaveHook is a callback function which will be invoked after each sync wave is successfully
// applied during a sync operation. The callback indicates which phase and wave it had just
// executed, and whether or not that wave was the final one.
type SyncWaveHook func(phase SyncPhase, wave int, final bool) error

const (
	SyncPhasePreSync  = "PreSync"
	SyncPhaseSync     = "Sync"
	SyncPhasePostSync = "PostSync"
	SyncPhaseSyncFail = "SyncFail"
)

// GroupVersionKind returns the GVK schema information for a given resource within a sync result
func (r *ResourceResult) GroupVersionKind() schema.GroupVersionKind {
	return schema.GroupVersionKind{
		Group:   r.Group,
		Version: r.Version,
		Kind:    r.Kind,
	}
}

// ResourceResults defines a list of resource results for a given operation
type ResourceResults []*ResourceResult

// Find returns the operation result for a specified resource and the index in the list where it was found
func (r ResourceResults) Find(group string, kind string, namespace string, name string, phase SyncPhase) (int, *ResourceResult) {
	for i, res := range r {
		if res.Group == group && res.Kind == kind && res.Namespace == namespace && res.Name == name && res.SyncPhase == phase {
			return i, res
		}
	}
	return 0, nil
}

// PruningRequired returns a positive integer containing the number of resources that require pruning after an operation has been completed
func (r ResourceResults) PruningRequired() (num int) {
	for _, res := range r {
		// find all resources that require pruning but ignore resources marked for no pruning using sync-option Prune=false
		if res.Status == ResultCodePruneSkipped && !strings.Contains(res.Message, "no prune") {
			num++
		}
	}
	return num
}

// RevisionHistory contains history information about a previous sync
type RevisionHistory struct {
	// Revision holds the revision the sync was performed against
	Revision string `json:"revision,omitempty" protobuf:"bytes,2,opt,name=revision"`
	// DeployedAt holds the time the sync operation completed
	DeployedAt metav1.Time `json:"deployedAt" protobuf:"bytes,4,opt,name=deployedAt"`
	// ID is an auto incrementing identifier of the RevisionHistory
	ID int64 `json:"id" protobuf:"bytes,5,opt,name=id"`
	// Source is a reference to the application source used for the sync operation
	Source ApplicationSource `json:"source,omitempty" protobuf:"bytes,6,opt,name=source"`
	// DeployStartedAt holds the time the sync operation started
	DeployStartedAt *metav1.Time `json:"deployStartedAt,omitempty" protobuf:"bytes,7,opt,name=deployStartedAt"`
	// Sources is a reference to the application sources used for the sync operation
	Sources ApplicationSources `json:"sources,omitempty" protobuf:"bytes,8,opt,name=sources"`
	// Revisions holds the revision of each source in sources field the sync was performed against
	Revisions []string `json:"revisions,omitempty" protobuf:"bytes,9,opt,name=revisions"`
	// InitiatedBy contains information about who initiated the operations
	InitiatedBy OperationInitiator `json:"initiatedBy,omitempty" protobuf:"bytes,10,opt,name=initiatedBy"`
}

// ApplicationWatchEvent contains information about application change.
type ApplicationWatchEvent struct {
	Type watch.EventType `json:"type" protobuf:"bytes,1,opt,name=type,casttype=k8s.io/apimachinery/pkg/watch.EventType"`

	// Application is:
	//  * If Type is Added or Modified: the new state of the object.
	//  * If Type is Deleted: the state of the object immediately before deletion.
	//  * If Type is Error: *api.Status is recommended; other types may make sense
	//    depending on context.
	Application Application `json:"application" protobuf:"bytes,2,opt,name=application"`
}

// ApplicationList is list of Application resources
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type ApplicationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata" protobuf:"bytes,1,opt,name=metadata"`
	Items           []Application `json:"items" protobuf:"bytes,2,rep,name=items"`
}

// ComponentParameter contains information about component parameter value
type ComponentParameter struct {
	Component string `json:"component,omitempty" protobuf:"bytes,1,opt,name=component"`
	Name      string `json:"name" protobuf:"bytes,2,opt,name=name"`
	Value     string `json:"value" protobuf:"bytes,3,opt,name=value"`
}

// SyncStatusCode is a type which represents possible comparison results
type SyncStatusCode string

// Possible comparison results
const (
	// SyncStatusCodeUnknown indicates that the status of a sync could not be reliably determined
	SyncStatusCodeUnknown SyncStatusCode = "Unknown"
	// SyncStatusCodeSynced indicates that desired and live states match
	SyncStatusCodeSynced SyncStatusCode = "Synced"
	// SyncStatusCodeOutOfSync indicates that there is a drift between desired and live states
	SyncStatusCodeOutOfSync SyncStatusCode = "OutOfSync"
)

// ApplicationConditionType represents type of application condition. Type name has following convention:
// prefix "Error" means error condition
// prefix "Warning" means warning condition
// prefix "Info" means informational condition
type ApplicationConditionType = string

const (
	// ApplicationConditionDeletionError indicates that controller failed to delete application
	ApplicationConditionDeletionError = "DeletionError"
	// ApplicationConditionInvalidSpecError indicates that application source is invalid
	ApplicationConditionInvalidSpecError = "InvalidSpecError"
	// ApplicationConditionComparisonError indicates controller failed to compare application state
	ApplicationConditionComparisonError = "ComparisonError"
	// ApplicationConditionSyncError indicates controller failed to automatically sync the application
	ApplicationConditionSyncError = "SyncError"
	// ApplicationConditionUnknownError indicates an unknown controller error
	ApplicationConditionUnknownError = "UnknownError"
	// ApplicationConditionSharedResourceWarning indicates that controller detected resources which belongs to more than one application
	ApplicationConditionSharedResourceWarning = "SharedResourceWarning"
	// ApplicationConditionRepeatedResourceWarning indicates that application source has resource with same Group, Kind, Name, Namespace multiple times
	ApplicationConditionRepeatedResourceWarning = "RepeatedResourceWarning"
	// ApplicationConditionExcludedResourceWarning indicates that application has resource which is configured to be excluded
	ApplicationConditionExcludedResourceWarning = "ExcludedResourceWarning"
	// ApplicationConditionOrphanedResourceWarning indicates that application has orphaned resources
	ApplicationConditionOrphanedResourceWarning = "OrphanedResourceWarning"
)

// ApplicationCondition contains details about an application condition, which is usually an error or warning
type ApplicationCondition struct {
	// Type is an application condition type
	Type ApplicationConditionType `json:"type" protobuf:"bytes,1,opt,name=type"`
	// Message contains human-readable message indicating details about condition
	Message string `json:"message" protobuf:"bytes,2,opt,name=message"`
	// LastTransitionTime is the time the condition was last observed
	LastTransitionTime *metav1.Time `json:"lastTransitionTime,omitempty" protobuf:"bytes,3,opt,name=lastTransitionTime"`
}

// ComparedTo contains application source and target which was used for resources comparison
type ComparedTo struct {
	// Source is a reference to the application's source used for comparison
	Source ApplicationSource `json:"source,omitempty" protobuf:"bytes,1,opt,name=source"`
	// Destination is a reference to the application's destination used for comparison
	Destination ApplicationDestination `json:"destination" protobuf:"bytes,2,opt,name=destination"`
	// Sources is a reference to the application's multiple sources used for comparison
	Sources ApplicationSources `json:"sources,omitempty" protobuf:"bytes,3,opt,name=sources"`
	// IgnoreDifferences is a reference to the application's ignored differences used for comparison
	IgnoreDifferences IgnoreDifferences `json:"ignoreDifferences,omitempty" protobuf:"bytes,4,opt,name=ignoreDifferences"`
}

// SyncStatus contains information about the currently observed live and desired states of an application
type SyncStatus struct {
	// Status is the sync state of the comparison
	Status SyncStatusCode `json:"status" protobuf:"bytes,1,opt,name=status,casttype=SyncStatusCode"`
	// ComparedTo contains information about what has been compared
	ComparedTo ComparedTo `json:"comparedTo,omitempty" protobuf:"bytes,2,opt,name=comparedTo"`
	// Revision contains information about the revision the comparison has been performed to
	Revision string `json:"revision,omitempty" protobuf:"bytes,3,opt,name=revision"`
	// Revisions contains information about the revisions of multiple sources the comparison has been performed to
	Revisions []string `json:"revisions,omitempty" protobuf:"bytes,4,opt,name=revisions"`
}

// HealthStatus contains information about the currently observed health state of an application or resource
type HealthStatus struct {
	// Status holds the status code of the application or resource
	Status HealthStatusCode `json:"status,omitempty" protobuf:"bytes,1,opt,name=status"`
	// Message is a human-readable informational message describing the health status
	Message string `json:"message,omitempty" protobuf:"bytes,2,opt,name=message"`
	// LastTransitionTime is the time the HealthStatus was set or updated
	LastTransitionTime *metav1.Time `json:"lastTransitionTime,omitempty" protobuf:"bytes,3,opt,name=lastTransitionTime"`
}

// Represents resource health status
type HealthStatusCode string

const (
	// Indicates that health assessment failed and actual health status is unknown
	HealthStatusUnknown HealthStatusCode = "Unknown"
	// Progressing health status means that resource is not healthy but still have a chance to reach healthy state
	HealthStatusProgressing HealthStatusCode = "Progressing"
	// Resource is 100% healthy
	HealthStatusHealthy HealthStatusCode = "Healthy"
	// Assigned to resources that are suspended or paused. The typical example is a
	// [suspended](https://kubernetes.io/docs/tasks/job/automated-tasks-with-cron-jobs/#suspend) CronJob.
	HealthStatusSuspended HealthStatusCode = "Suspended"
	// Degrade status is used if resource status indicates failure or resource could not reach healthy state
	// within some timeout.
	HealthStatusDegraded HealthStatusCode = "Degraded"
	// Indicates that resource is missing in the cluster.
	HealthStatusMissing HealthStatusCode = "Missing"
)

// InfoItem contains arbitrary, human readable information about an application
type InfoItem struct {
	// Name is a human readable title for this piece of information.
	Name string `json:"name,omitempty" protobuf:"bytes,1,opt,name=name"`
	// Value is human readable content.
	Value string `json:"value,omitempty" protobuf:"bytes,2,opt,name=value"`
}

// ResourceNetworkingInfo holds networking-related information for a resource.
type ResourceNetworkingInfo struct {
	// TargetLabels represents labels associated with the target resources that this resource communicates with.
	TargetLabels map[string]string `json:"targetLabels,omitempty" protobuf:"bytes,1,opt,name=targetLabels"`
	// TargetRefs contains references to other resources that this resource interacts with, such as Services or Pods.
	TargetRefs []ResourceRef `json:"targetRefs,omitempty" protobuf:"bytes,2,opt,name=targetRefs"`
	// Labels holds the labels associated with this networking resource.
	Labels map[string]string `json:"labels,omitempty" protobuf:"bytes,3,opt,name=labels"`
	// Ingress provides information about external access points (e.g., load balancer ingress) for this resource.
	Ingress []corev1.LoadBalancerIngress `json:"ingress,omitempty" protobuf:"bytes,4,opt,name=ingress"`
	// ExternalURLs holds a list of URLs that should be accessible externally.
	// This field is typically populated for Ingress resources based on their hostname rules.
	ExternalURLs []string `json:"externalURLs,omitempty" protobuf:"bytes,5,opt,name=externalURLs"`
}

// HostResourceInfo represents resource usage details for a specific resource type on a host.
type HostResourceInfo struct {
	// ResourceName specifies the type of resource (e.g., CPU, memory, storage).
	ResourceName corev1.ResourceName `json:"resourceName,omitempty" protobuf:"bytes,1,name=resourceName"`
	// RequestedByApp indicates the total amount of this resource requested by the application running on the host.
	RequestedByApp int64 `json:"requestedByApp,omitempty" protobuf:"bytes,2,name=requestedByApp"`
	// RequestedByNeighbors indicates the total amount of this resource requested by other workloads on the same host.
	RequestedByNeighbors int64 `json:"requestedByNeighbors,omitempty" protobuf:"bytes,3,name=requestedByNeighbors"`
	// Capacity represents the total available capacity of this resource on the host.
	Capacity int64 `json:"capacity,omitempty" protobuf:"bytes,4,name=capacity"`
}

// HostInfo holds metadata and resource usage metrics for a specific host in the cluster.
type HostInfo struct {
	// Name is the hostname or node name in the Kubernetes cluster.
	Name string `json:"name,omitempty" protobuf:"bytes,1,name=name"`
	// ResourcesInfo provides a list of resource usage details for different resource types on this host.
	ResourcesInfo []HostResourceInfo `json:"resourcesInfo,omitempty" protobuf:"bytes,2,name=resourcesInfo"`
	// SystemInfo contains detailed system-level information about the host, such as OS, kernel version, and architecture.
	SystemInfo corev1.NodeSystemInfo `json:"systemInfo,omitempty" protobuf:"bytes,3,opt,name=systemInfo"`
}

// ApplicationTree represents the hierarchical structure of resources associated with an Argo CD application.
type ApplicationTree struct {
	// Nodes contains a list of resources that are either directly managed by the application
	// or are children of directly managed resources.
	Nodes []ResourceNode `json:"nodes,omitempty" protobuf:"bytes,1,rep,name=nodes"`
	// OrphanedNodes contains resources that exist in the same namespace as the application
	// but are not managed by it. This list is populated only if orphaned resource tracking
	// is enabled in the application's project settings.
	OrphanedNodes []ResourceNode `json:"orphanedNodes,omitempty" protobuf:"bytes,2,rep,name=orphanedNodes"`
	// Hosts provides a list of Kubernetes nodes that are running pods related to the application.
	Hosts []HostInfo `json:"hosts,omitempty" protobuf:"bytes,3,rep,name=hosts"`
	// ShardsCount represents the total number of shards the application tree is split into.
	// This is used to distribute resource processing across multiple shards.
	ShardsCount int64 `json:"shardsCount,omitempty" protobuf:"bytes,4,opt,name=shardsCount"`
}

func (t *ApplicationTree) Merge(other *ApplicationTree) {
	t.Nodes = append(t.Nodes, other.Nodes...)
	t.OrphanedNodes = append(t.OrphanedNodes, other.OrphanedNodes...)
	t.Hosts = append(t.Hosts, other.Hosts...)
	t.Normalize()
}

// GetShards split application tree into shards with populated metadata
func (t *ApplicationTree) GetShards(size int64) []*ApplicationTree {
	t.Normalize()
	if size == 0 {
		return []*ApplicationTree{t}
	}

	var items []func(*ApplicationTree)
	for i := range t.Nodes {
		item := t.Nodes[i]
		items = append(items, func(shard *ApplicationTree) {
			shard.Nodes = append(shard.Nodes, item)
		})
	}
	for i := range t.OrphanedNodes {
		item := t.OrphanedNodes[i]
		items = append(items, func(shard *ApplicationTree) {
			shard.OrphanedNodes = append(shard.OrphanedNodes, item)
		})
	}
	for i := range t.Hosts {
		item := t.Hosts[i]
		items = append(items, func(shard *ApplicationTree) {
			shard.Hosts = append(shard.Hosts, item)
		})
	}
	var shards []*ApplicationTree
	for len(items) > 0 {
		shard := &ApplicationTree{}
		shards = append(shards, shard)
		cnt := 0
		for i := int64(0); i < size && i < int64(len(items)); i++ {
			items[i](shard)
			cnt++
		}
		items = items[cnt:]
	}
	if len(shards) > 0 {
		shards[0].ShardsCount = int64(len(shards))
	} else {
		shards = []*ApplicationTree{{ShardsCount: 0}}
	}
	return shards
}

// Normalize sorts application tree nodes and hosts. The persistent order allows to
// effectively compare previously cached app tree and allows to unnecessary Redis requests.
func (t *ApplicationTree) Normalize() {
	sort.Slice(t.Nodes, func(i, j int) bool {
		return t.Nodes[i].FullName() < t.Nodes[j].FullName()
	})
	sort.Slice(t.OrphanedNodes, func(i, j int) bool {
		return t.OrphanedNodes[i].FullName() < t.OrphanedNodes[j].FullName()
	})
	sort.Slice(t.Hosts, func(i, j int) bool {
		return t.Hosts[i].Name < t.Hosts[j].Name
	})
}

// ApplicationSummary contains information about URLs and container images used by an application
type ApplicationSummary struct {
	// ExternalURLs holds all external URLs of application child resources.
	ExternalURLs []string `json:"externalURLs,omitempty" protobuf:"bytes,1,opt,name=externalURLs"`
	// Images holds all images of application child resources.
	Images []string `json:"images,omitempty" protobuf:"bytes,2,opt,name=images"`
}

// FindNode searches for a resource node in the application tree.
// It looks for a node that matches the given group, kind, namespace, and name.
// The search includes both directly managed nodes (`Nodes`) and orphaned nodes (`OrphanedNodes`).
// Returns a pointer to the found node, or nil if no matching node is found.
func (t *ApplicationTree) FindNode(group string, kind string, namespace string, name string) *ResourceNode {
	for _, n := range append(t.Nodes, t.OrphanedNodes...) {
		if n.Group == group && n.Kind == kind && n.Namespace == namespace && n.Name == name {
			return &n
		}
	}
	return nil
}

// GetSummary generates a summary of the application by extracting external URLs and container images
// used within the application's resources.
//
// - It collects external URLs from the networking information of all resource nodes.
// - It extracts container images referenced in the application's resources.
// - Additionally, it includes links from application annotations that start with `common.AnnotationKeyLinkPrefix`.
// - The collected URLs and images are sorted alphabetically before being returned.
//
// Returns an `ApplicationSummary` containing a list of unique external URLs and container images.
func (t *ApplicationTree) GetSummary(app *Application) ApplicationSummary {
	urlsSet := make(map[string]bool)
	imagesSet := make(map[string]bool)

	// Collect external URLs and container images from application nodes
	for _, node := range t.Nodes {
		if node.NetworkingInfo != nil {
			for _, url := range node.NetworkingInfo.ExternalURLs {
				urlsSet[url] = true
			}
		}
		for _, image := range node.Images {
			imagesSet[image] = true
		}
	}

	// Include application-specific links from annotations
	for k, v := range app.GetAnnotations() {
		if strings.HasPrefix(k, AnnotationKeyLinkPrefix) {
			urlsSet[v] = true
		}
	}

	urls := make([]string, 0, len(urlsSet))
	for url := range urlsSet {
		urls = append(urls, url)
	}
	sort.Strings(urls)

	images := make([]string, 0, len(imagesSet))
	for image := range imagesSet {
		images = append(images, image)
	}
	sort.Strings(images)

	return ApplicationSummary{ExternalURLs: urls, Images: images}
}

// ResourceRef includes fields which uniquely identify a resource
type ResourceRef struct {
	Group     string `json:"group,omitempty" protobuf:"bytes,1,opt,name=group"`
	Version   string `json:"version,omitempty" protobuf:"bytes,2,opt,name=version"`
	Kind      string `json:"kind,omitempty" protobuf:"bytes,3,opt,name=kind"`
	Namespace string `json:"namespace,omitempty" protobuf:"bytes,4,opt,name=namespace"`
	Name      string `json:"name,omitempty" protobuf:"bytes,5,opt,name=name"`
	UID       string `json:"uid,omitempty" protobuf:"bytes,6,opt,name=uid"`
}

// ResourceNode contains information about a live Kubernetes resource and its relationships with other resources.
type ResourceNode struct {
	// ResourceRef uniquely identifies the resource using its group, kind, namespace, and name.
	ResourceRef `json:",inline" protobuf:"bytes,1,opt,name=resourceRef"`
	// ParentRefs lists the parent resources that reference this resource.
	// This helps in understanding ownership and hierarchical relationships.
	ParentRefs []ResourceRef `json:"parentRefs,omitempty" protobuf:"bytes,2,opt,name=parentRefs"`
	// Info provides additional metadata or annotations about the resource.
	Info []InfoItem `json:"info,omitempty" protobuf:"bytes,3,opt,name=info"`
	// NetworkingInfo contains details about the resource's networking attributes,
	// such as ingress information and external URLs.
	NetworkingInfo *ResourceNetworkingInfo `json:"networkingInfo,omitempty" protobuf:"bytes,4,opt,name=networkingInfo"`
	// ResourceVersion indicates the version of the resource, used to track changes.
	ResourceVersion string `json:"resourceVersion,omitempty" protobuf:"bytes,5,opt,name=resourceVersion"`
	// Images lists container images associated with the resource.
	// This is primarily useful for pods and other workload resources.
	Images []string `json:"images,omitempty" protobuf:"bytes,6,opt,name=images"`
	// Health represents the health status of the resource (e.g., Healthy, Degraded, Progressing).
	Health *HealthStatus `json:"health,omitempty" protobuf:"bytes,7,opt,name=health"`
	// CreatedAt records the timestamp when the resource was created.
	CreatedAt *metav1.Time `json:"createdAt,omitempty" protobuf:"bytes,8,opt,name=createdAt"`
}

// FullName returns a resource node's full name in the format "group/kind/namespace/name"
// For cluster-scoped resources, namespace will be the empty string.
func (n *ResourceNode) FullName() string {
	return fmt.Sprintf("%s/%s/%s/%s", n.Group, n.Kind, n.Namespace, n.Name)
}

// GroupKindVersion returns the GVK schema type for given resource node
func (n *ResourceNode) GroupKindVersion() schema.GroupVersionKind {
	return schema.GroupVersionKind{
		Group:   n.Group,
		Version: n.Version,
		Kind:    n.Kind,
	}
}

// ResourceStatus holds the current synchronization and health status of a Kubernetes resource.
type ResourceStatus struct {
	// Group represents the API group of the resource (e.g., "apps" for Deployments).
	Group string `json:"group,omitempty" protobuf:"bytes,1,opt,name=group"`
	// Version indicates the API version of the resource (e.g., "v1", "v1beta1").
	Version string `json:"version,omitempty" protobuf:"bytes,2,opt,name=version"`
	// Kind specifies the type of the resource (e.g., "Deployment", "Service").
	Kind string `json:"kind,omitempty" protobuf:"bytes,3,opt,name=kind"`
	// Namespace defines the Kubernetes namespace where the resource is located.
	Namespace string `json:"namespace,omitempty" protobuf:"bytes,4,opt,name=namespace"`
	// Name is the unique name of the resource within the namespace.
	Name string `json:"name,omitempty" protobuf:"bytes,5,opt,name=name"`
	// Status represents the synchronization state of the resource (e.g., Synced, OutOfSync).
	Status SyncStatusCode `json:"status,omitempty" protobuf:"bytes,6,opt,name=status"`
	// Health indicates the health status of the resource (e.g., Healthy, Degraded, Progressing).
	Health *HealthStatus `json:"health,omitempty" protobuf:"bytes,7,opt,name=health"`
	// Hook is true if the resource is used as a lifecycle hook in an Argo CD application.
	Hook bool `json:"hook,omitempty" protobuf:"bytes,8,opt,name=hook"`
	// RequiresPruning is true if the resource needs to be pruned (deleted) as part of synchronization.
	RequiresPruning bool `json:"requiresPruning,omitempty" protobuf:"bytes,9,opt,name=requiresPruning"`
	// SyncWave determines the order in which resources are applied during a sync operation.
	// Lower values are applied first.
	SyncWave int64 `json:"syncWave,omitempty" protobuf:"bytes,10,opt,name=syncWave"`
	// RequiresDeletionConfirmation is true if the resource requires explicit user confirmation before deletion.
	RequiresDeletionConfirmation bool `json:"requiresDeletionConfirmation,omitempty" protobuf:"bytes,11,opt,name=requiresDeletionConfirmation"`
}

// GroupVersionKind returns the GVK schema type for given resource status
func (r *ResourceStatus) GroupVersionKind() schema.GroupVersionKind {
	return schema.GroupVersionKind{Group: r.Group, Version: r.Version, Kind: r.Kind}
}

// ResourceDiff holds the diff between a live and target resource object in Argo CD.
// It is used to compare the desired state (from Git/Helm) with the actual state in the cluster.
type ResourceDiff struct {
	// Group represents the API group of the resource (e.g., "apps" for Deployments).
	Group string `json:"group,omitempty" protobuf:"bytes,1,opt,name=group"`
	// Kind represents the Kubernetes resource kind (e.g., "Deployment", "Service").
	Kind string `json:"kind,omitempty" protobuf:"bytes,2,opt,name=kind"`
	// Namespace specifies the namespace where the resource exists.
	Namespace string `json:"namespace,omitempty" protobuf:"bytes,3,opt,name=namespace"`
	// Name is the name of the resource.
	Name string `json:"name,omitempty" protobuf:"bytes,4,opt,name=name"`
	// TargetState contains the JSON-serialized resource manifest as defined in the Git/Helm repository.
	TargetState string `json:"targetState,omitempty" protobuf:"bytes,5,opt,name=targetState"`
	// LiveState contains the JSON-serialized resource manifest of the resource currently running in the cluster.
	LiveState string `json:"liveState,omitempty" protobuf:"bytes,6,opt,name=liveState"`
	// Diff contains the JSON patch representing the difference between the live and target resource.
	// Deprecated: Use NormalizedLiveState and PredictedLiveState instead to compute differences.
	Diff string `json:"diff,omitempty" protobuf:"bytes,7,opt,name=diff"`
	// Hook indicates whether this resource is a hook resource (e.g., pre-sync or post-sync hooks).
	Hook bool `json:"hook,omitempty" protobuf:"bytes,8,opt,name=hook"`
	// NormalizedLiveState contains the JSON-serialized live resource state after applying normalizations.
	// Normalizations may include ignoring irrelevant fields like timestamps or defaults applied by Kubernetes.
	NormalizedLiveState string `json:"normalizedLiveState,omitempty" protobuf:"bytes,9,opt,name=normalizedLiveState"`
	// PredictedLiveState contains the JSON-serialized resource state that Argo CD predicts based on the
	// combination of the normalized live state and the desired target state.
	PredictedLiveState string `json:"predictedLiveState,omitempty" protobuf:"bytes,10,opt,name=predictedLiveState"`
	// ResourceVersion is the Kubernetes resource version, which helps in tracking changes.
	ResourceVersion string `json:"resourceVersion,omitempty" protobuf:"bytes,11,opt,name=resourceVersion"`
	// Modified indicates whether the live resource has changes compared to the target resource.
	Modified bool `json:"modified,omitempty" protobuf:"bytes,12,opt,name=modified"`
}

// FullName returns full name of a node that was used for diffing in the format "group/kind/namespace/name"
// For cluster-scoped resources, namespace will be the empty string.
func (r *ResourceDiff) FullName() string {
	return fmt.Sprintf("%s/%s/%s/%s", r.Group, r.Kind, r.Namespace, r.Name)
}

// ConnectionStatus represents the status indicator for a connection to a remote resource
type ConnectionStatus = string

const (
	// ConnectionStatusSuccessful indicates that a connection has been successfully established
	ConnectionStatusSuccessful = "Successful"
	// ConnectionStatusFailed indicates that a connection attempt has failed
	ConnectionStatusFailed = "Failed"
	// ConnectionStatusUnknown indicates that the connection status could not be reliably determined
	ConnectionStatusUnknown = "Unknown"
)

// ConnectionState contains information about remote resource connection state, currently used for clusters and repositories
type ConnectionState struct {
	// Status contains the current status indicator for the connection
	Status ConnectionStatus `json:"status" protobuf:"bytes,1,opt,name=status"`
	// Message contains human readable information about the connection status
	Message string `json:"message" protobuf:"bytes,2,opt,name=message"`
	// ModifiedAt contains the timestamp when this connection status has been determined
	ModifiedAt *metav1.Time `json:"attemptedAt" protobuf:"bytes,3,opt,name=attemptedAt"`
}

// AWSAuthConfig is an AWS IAM authentication configuration
type AWSAuthConfig struct {
	// ClusterName contains AWS cluster name
	ClusterName string `json:"clusterName,omitempty" protobuf:"bytes,1,opt,name=clusterName"`

	// RoleARN contains optional role ARN. If set then AWS IAM Authenticator assume a role to perform cluster operations instead of the default AWS credential provider chain.
	RoleARN string `json:"roleARN,omitempty" protobuf:"bytes,2,opt,name=roleARN"`

	// Profile contains optional role ARN. If set then AWS IAM Authenticator uses the profile to perform cluster operations instead of the default AWS credential provider chain.
	Profile string `json:"profile,omitempty" protobuf:"bytes,3,opt,name=profile"`
}

// ExecProviderConfig is config used to call an external command to perform cluster authentication
// See: https://godoc.org/k8s.io/client-go/tools/clientcmd/api#ExecConfig
type ExecProviderConfig struct {
	// Command to execute
	Command string `json:"command,omitempty" protobuf:"bytes,1,opt,name=command"`

	// Arguments to pass to the command when executing it
	Args []string `json:"args,omitempty" protobuf:"bytes,2,rep,name=args"`

	// Env defines additional environment variables to expose to the process
	Env map[string]string `json:"env,omitempty" protobuf:"bytes,3,opt,name=env"`

	// Preferred input version of the ExecInfo
	APIVersion string `json:"apiVersion,omitempty" protobuf:"bytes,4,opt,name=apiVersion"`

	// This text is shown to the user when the executable doesn't seem to be present
	InstallHint string `json:"installHint,omitempty" protobuf:"bytes,5,opt,name=installHint"`
}

// KnownTypeField contains a mapping between a Custom Resource Definition (CRD) field
// and a well-known Kubernetes type. This mapping is primarily used for unit conversions
// in resources where the type is not explicitly defined (e.g., converting "0.1" to "100m" for CPU requests).
type KnownTypeField struct {
	// Field represents the JSON path to the specific field in the CRD that requires type conversion.
	// Example: "spec.resources.requests.cpu"
	Field string `json:"field,omitempty" protobuf:"bytes,1,opt,name=field"`
	// Type specifies the expected Kubernetes type for the field, such as "cpu" or "memory".
	// This helps in converting values between different formats (e.g., "0.1" to "100m" for CPU).
	Type string `json:"type,omitempty" protobuf:"bytes,2,opt,name=type"`
}

// OverrideIgnoreDiff contains configurations about how fields should be ignored during diffs between
// the desired state and live state
type OverrideIgnoreDiff struct {
	// JSONPointers is a JSON path list following the format defined in RFC4627 (https://datatracker.ietf.org/doc/html/rfc6902#section-3)
	JSONPointers []string `json:"jsonPointers" protobuf:"bytes,1,rep,name=jSONPointers"`
	// JQPathExpressions is a JQ path list that will be evaludated during the diff process
	JQPathExpressions []string `json:"jqPathExpressions" protobuf:"bytes,2,opt,name=jqPathExpressions"`
	// ManagedFieldsManagers is a list of trusted managers. Fields mutated by those managers will take precedence over the
	// desired state defined in the SCM and won't be displayed in diffs
	ManagedFieldsManagers []string `json:"managedFieldsManagers" protobuf:"bytes,3,opt,name=managedFieldsManagers"`
}

type rawResourceOverride struct {
	HealthLua             string           `json:"health.lua,omitempty"`
	UseOpenLibs           bool             `json:"health.lua.useOpenLibs,omitempty"`
	Actions               string           `json:"actions,omitempty"`
	IgnoreDifferences     string           `json:"ignoreDifferences,omitempty"`
	IgnoreResourceUpdates string           `json:"ignoreResourceUpdates,omitempty"`
	KnownTypeFields       []KnownTypeField `json:"knownTypeFields,omitempty"`
}

// ResourceOverride holds configuration to customize resource diffing and health assessment
type ResourceOverride struct {
	// HealthLua contains a Lua script that defines custom health checks for the resource.
	HealthLua string `protobuf:"bytes,1,opt,name=healthLua"`
	// UseOpenLibs indicates whether to use open-source libraries for the resource.
	UseOpenLibs bool `protobuf:"bytes,5,opt,name=useOpenLibs"`
	// Actions defines the set of actions that can be performed on the resource, as a Lua script.
	Actions string `protobuf:"bytes,3,opt,name=actions"`
	// IgnoreDifferences contains configuration for which differences should be ignored during the resource diffing.
	IgnoreDifferences OverrideIgnoreDiff `protobuf:"bytes,2,opt,name=ignoreDifferences"`
	// IgnoreResourceUpdates holds configuration for ignoring updates to specific resource fields.
	IgnoreResourceUpdates OverrideIgnoreDiff `protobuf:"bytes,6,opt,name=ignoreResourceUpdates"`
	// KnownTypeFields lists fields for which unit conversions should be applied.
	KnownTypeFields []KnownTypeField `protobuf:"bytes,4,opt,name=knownTypeFields"`
}

// UnmarshalJSON unmarshals a JSON byte slice into a ResourceOverride object.
// It parses the raw input data and handles special processing for `IgnoreDifferences`
// and `IgnoreResourceUpdates` fields using YAML format.
func (ro *ResourceOverride) UnmarshalJSON(data []byte) error {
	raw := &rawResourceOverride{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	ro.KnownTypeFields = raw.KnownTypeFields
	ro.HealthLua = raw.HealthLua
	ro.UseOpenLibs = raw.UseOpenLibs
	ro.Actions = raw.Actions
	err := yaml.Unmarshal([]byte(raw.IgnoreDifferences), &ro.IgnoreDifferences)
	if err != nil {
		return err
	}
	err = yaml.Unmarshal([]byte(raw.IgnoreResourceUpdates), &ro.IgnoreResourceUpdates)
	if err != nil {
		return err
	}
	return nil
}

// MarshalJSON marshals a ResourceOverride object into a JSON byte slice.
// It converts `IgnoreDifferences` and `IgnoreResourceUpdates` fields to YAML format before marshaling.
func (ro ResourceOverride) MarshalJSON() ([]byte, error) {
	ignoreDifferencesData, err := yaml.Marshal(ro.IgnoreDifferences)
	if err != nil {
		return nil, err
	}
	ignoreResourceUpdatesData, err := yaml.Marshal(ro.IgnoreResourceUpdates)
	if err != nil {
		return nil, err
	}
	raw := &rawResourceOverride{ro.HealthLua, ro.UseOpenLibs, ro.Actions, string(ignoreDifferencesData), string(ignoreResourceUpdatesData), ro.KnownTypeFields}
	return json.Marshal(raw)
}

// GetActions parses and returns the actions defined for the resource.
// It unmarshals the `Actions` field (a Lua script) into a ResourceActions object.
func (ro *ResourceOverride) GetActions() (ResourceActions, error) {
	var actions ResourceActions
	err := yaml.Unmarshal([]byte(ro.Actions), &actions)
	if err != nil {
		return actions, err
	}
	return actions, nil
}

// ResourceActions holds the set of actions that can be applied to a resource.
// It defines custom Lua scripts for discovery and action execution, as well as options
// for merging built-in actions with custom ones.
type ResourceActions struct {
	// ActionDiscoveryLua contains a Lua script for discovering actions.
	ActionDiscoveryLua string `json:"discovery.lua,omitempty" yaml:"discovery.lua,omitempty" protobuf:"bytes,1,opt,name=actionDiscoveryLua"`
	// Definitions holds the list of action definitions available for the resource.
	Definitions []ResourceActionDefinition `json:"definitions,omitempty" protobuf:"bytes,2,rep,name=definitions"`
	// MergeBuiltinActions indicates whether built-in actions should be merged with custom actions.
	MergeBuiltinActions bool `json:"mergeBuiltinActions,omitempty" yaml:"mergeBuiltinActions,omitempty" protobuf:"bytes,3,opt,name=mergeBuiltinActions"`
}

// ResourceActionDefinition defines an individual action that can be executed on a resource.
// It includes a name for the action and a Lua script that defines the action's behavior.
type ResourceActionDefinition struct {
	// Name is the identifier for the action.
	Name string `json:"name" protobuf:"bytes,1,opt,name=name"`
	// ActionLua contains the Lua script that defines the behavior of the action.
	ActionLua string `json:"action.lua" yaml:"action.lua" protobuf:"bytes,2,opt,name=actionLua"`
}

// ResourceAction represents an individual action that can be performed on a resource.
// It includes parameters, an optional disabled flag, an icon for display, and a name for the action.
type ResourceAction struct {
	// Name is the name or identifier for the action.
	Name string `json:"name,omitempty" protobuf:"bytes,1,opt,name=name"`
	// Params contains the parameters required to execute the action.
	Params []ResourceActionParam `json:"params,omitempty" protobuf:"bytes,2,rep,name=params"`
	// Disabled indicates whether the action is disabled.
	Disabled bool `json:"disabled,omitempty" protobuf:"varint,3,opt,name=disabled"`
	// IconClass specifies the CSS class for the action's icon.
	IconClass string `json:"iconClass,omitempty" protobuf:"bytes,4,opt,name=iconClass"`
	// DisplayName provides a user-friendly name for the action.
	DisplayName string `json:"displayName,omitempty" protobuf:"bytes,5,opt,name=displayName"`
}

// ResourceActionParam represents a parameter for a resource action.
// It includes a name, value, type, and an optional default value for the parameter.
type ResourceActionParam struct {
	// Name is the name of the parameter.
	Name string `json:"name,omitempty" protobuf:"bytes,1,opt,name=name"`
	// Value is the value of the parameter.
	Value string `json:"value,omitempty" protobuf:"bytes,2,opt,name=value"`
	// Type is the type of the parameter (e.g., string, integer).
	Type string `json:"type,omitempty" protobuf:"bytes,3,opt,name=type"`
	// Default is the default value of the parameter, if any.
	Default string `json:"default,omitempty" protobuf:"bytes,4,opt,name=default"`
}

// OrphanedResourcesMonitorSettings holds settings of orphaned resources monitoring
type OrphanedResourcesMonitorSettings struct {
	// Warn indicates if warning condition should be created for apps which have orphaned resources
	Warn *bool `json:"warn,omitempty" protobuf:"bytes,1,name=warn"`
	// Ignore contains a list of resources that are to be excluded from orphaned resources monitoring
	Ignore []OrphanedResourceKey `json:"ignore,omitempty" protobuf:"bytes,2,opt,name=ignore"`
}

// OrphanedResourceKey is a reference to a resource to be ignored from
type OrphanedResourceKey struct {
	Group string `json:"group,omitempty" protobuf:"bytes,1,opt,name=group"`
	Kind  string `json:"kind,omitempty" protobuf:"bytes,2,opt,name=kind"`
	Name  string `json:"name,omitempty" protobuf:"bytes,3,opt,name=name"`
}

// IsWarn returns true if warnings are enabled for orphan resources monitoring
func (s *OrphanedResourcesMonitorSettings) IsWarn() bool {
	return s.Warn != nil && *s.Warn
}

// SignatureKey is the specification of a key required to verify commit signatures with
type SignatureKey struct {
	// The ID of the key in hexadecimal notation
	KeyID string `json:"keyID" protobuf:"bytes,1,name=keyID"`
}

// ProjectRole represents a role that has access to a project
type ProjectRole struct {
	// Name is a name for this role
	Name string `json:"name" protobuf:"bytes,1,opt,name=name"`
	// Description is a description of the role
	Description string `json:"description,omitempty" protobuf:"bytes,2,opt,name=description"`
	// Policies Stores a list of casbin formatted strings that define access policies for the role in the project
	Policies []string `json:"policies,omitempty" protobuf:"bytes,3,rep,name=policies"`
	// JWTTokens are a list of generated JWT tokens bound to this role
	JWTTokens []JWTToken `json:"jwtTokens,omitempty" protobuf:"bytes,4,rep,name=jwtTokens"`
	// Groups are a list of OIDC group claims bound to this role
	Groups []string `json:"groups,omitempty" protobuf:"bytes,5,rep,name=groups"`
}

// JWTToken holds the issuedAt and expiresAt values of a token
type JWTToken struct {
	IssuedAt  int64  `json:"iat" protobuf:"int64,1,opt,name=iat"`
	ExpiresAt int64  `json:"exp,omitempty" protobuf:"int64,2,opt,name=exp"`
	ID        string `json:"id,omitempty" protobuf:"bytes,3,opt,name=id"`
}

// Command holds binary path and arguments list
type Command struct {
	Command []string `json:"command,omitempty" protobuf:"bytes,1,name=command"`
	Args    []string `json:"args,omitempty" protobuf:"bytes,2,rep,name=args"`
}

// ConfigManagementPlugin contains config management plugin configuration
type ConfigManagementPlugin struct {
	Name     string   `json:"name" protobuf:"bytes,1,name=name"`
	Init     *Command `json:"init,omitempty" protobuf:"bytes,2,name=init"`
	Generate Command  `json:"generate" protobuf:"bytes,3,name=generate"`
	LockRepo bool     `json:"lockRepo,omitempty" protobuf:"bytes,4,name=lockRepo"`
}

// HelmOptions holds helm options
type HelmOptions struct {
	ValuesFileSchemes []string `protobuf:"bytes,1,opt,name=valuesFileSchemes"`
}

// KustomizeOptions are options for kustomize to use when building manifests
type KustomizeOptions struct {
	// BuildOptions is a string of build parameters to use when calling `kustomize build`
	BuildOptions string `protobuf:"bytes,1,opt,name=buildOptions"`
	// BinaryPath holds optional path to kustomize binary
	BinaryPath string `protobuf:"bytes,2,opt,name=binaryPath"`
}

// ApplicationDestinationServiceAccount holds information about the service account to be impersonated for the application sync operation.
type ApplicationDestinationServiceAccount struct {
	// Server specifies the URL of the target cluster's Kubernetes control plane API.
	Server string `json:"server" protobuf:"bytes,1,opt,name=server"`
	// Namespace specifies the target namespace for the application's resources.
	Namespace string `json:"namespace,omitempty" protobuf:"bytes,2,opt,name=namespace"`
	// DefaultServiceAccount to be used for impersonation during the sync operation
	DefaultServiceAccount string `json:"defaultServiceAccount" protobuf:"bytes,3,opt,name=defaultServiceAccount"`
}

// CascadedDeletion indicates if the deletion finalizer is set and controller should delete the application and it's cascaded resources
func (app *Application) CascadedDeletion() bool {
	for _, finalizer := range app.ObjectMeta.Finalizers {
		if isPropagationPolicyFinalizer(finalizer) {
			return true
		}
	}
	return false
}

// IsRefreshRequested returns whether a refresh has been requested for an application, and if yes, the type of refresh that should be executed.
func (app *Application) IsRefreshRequested() (RefreshType, bool) {
	refreshType := RefreshTypeNormal
	annotations := app.GetAnnotations()
	if annotations == nil {
		return refreshType, false
	}
	typeStr, ok := annotations[AnnotationKeyRefresh]
	if !ok {
		return refreshType, false
	}

	if typeStr == string(RefreshTypeHard) {
		refreshType = RefreshTypeHard
	}
	return refreshType, true
}

// IsHydrateRequested returns whether hydration has been requested for an application
func (app *Application) IsHydrateRequested() bool {
	annotations := app.GetAnnotations()
	if annotations == nil {
		return false
	}
	typeStr, ok := annotations[AnnotationKeyHydrate]
	if !ok {
		return false
	}
	if typeStr == string(HydrateTypeNormal) {
		return true
	}
	return false
}

func (app *Application) HasPostDeleteFinalizer(stage ...string) bool {
	return getFinalizerIndex(app.ObjectMeta, strings.Join(append([]string{PostDeleteFinalizerName}, stage...), "/")) > -1
}

func (app *Application) SetPostDeleteFinalizer(stage ...string) {
	setFinalizer(&app.ObjectMeta, strings.Join(append([]string{PostDeleteFinalizerName}, stage...), "/"), true)
}

func (app *Application) UnSetPostDeleteFinalizer(stage ...string) {
	setFinalizer(&app.ObjectMeta, strings.Join(append([]string{PostDeleteFinalizerName}, stage...), "/"), false)
}

// SetCascadedDeletion will enable cascaded deletion by setting the propagation policy finalizer
func (app *Application) SetCascadedDeletion(finalizer string) {
	setFinalizer(&app.ObjectMeta, finalizer, true)
}

// Expired returns true if the application needs to be reconciled
func (status *ApplicationStatus) Expired(statusRefreshTimeout time.Duration) bool {
	return status.ReconciledAt == nil || status.ReconciledAt.Add(statusRefreshTimeout).Before(time.Now().UTC())
}

// UnSetCascadedDeletion will remove the propagation policy finalizers
func (app *Application) UnSetCascadedDeletion() {
	for _, f := range app.Finalizers {
		if isPropagationPolicyFinalizer(f) {
			setFinalizer(&app.ObjectMeta, f, false)
		}
	}
}

func isPropagationPolicyFinalizer(finalizer string) bool {
	switch finalizer {
	case ResourcesFinalizerName:
		return true
	case ForegroundPropagationPolicyFinalizer:
		return true
	case BackgroundPropagationPolicyFinalizer:
		return true
	default:
		return false
	}
}

// GetPropagationPolicy returns the value of propagation policy finalizer
func (app *Application) GetPropagationPolicy() string {
	for _, finalizer := range app.ObjectMeta.Finalizers {
		if isPropagationPolicyFinalizer(finalizer) {
			return finalizer
		}
	}
	return ""
}

// HasChangedManagedNamespaceMetadata checks whether app.Spec.SyncPolicy.ManagedNamespaceMetadata differs from the
// managed namespace metadata which has been stored app.Status.OperationState.SyncResult. If they differ a refresh should
// be triggered.
func (app *Application) HasChangedManagedNamespaceMetadata() bool {
	return app.Spec.SyncPolicy != nil && app.Spec.SyncPolicy.ManagedNamespaceMetadata != nil && app.Status.OperationState != nil && app.Status.OperationState.SyncResult != nil && !reflect.DeepEqual(app.Spec.SyncPolicy.ManagedNamespaceMetadata, app.Status.OperationState.SyncResult.ManagedNamespaceMetadata)
}

// IsFinalizerPresent checks if the app has a given finalizer
func (app *Application) IsFinalizerPresent(finalizer string) bool {
	return getFinalizerIndex(app.ObjectMeta, finalizer) > -1
}

// SetConditions updates the application status conditions for a subset of evaluated types.
// If the application has a pre-existing condition of a type that is not in the evaluated list,
// it will be preserved. If the application has a pre-existing condition of a type that
// is in the evaluated list, but not in the incoming conditions list, it will be removed.
func (status *ApplicationStatus) SetConditions(conditions []ApplicationCondition, evaluatedTypes map[ApplicationConditionType]bool) {
	appConditions := make([]ApplicationCondition, 0)
	now := metav1.Now()
	for i := 0; i < len(status.Conditions); i++ {
		condition := status.Conditions[i]
		if _, ok := evaluatedTypes[condition.Type]; !ok {
			if condition.LastTransitionTime == nil {
				condition.LastTransitionTime = &now
			}
			appConditions = append(appConditions, condition)
		}
	}
	for i := range conditions {
		condition := conditions[i]
		if condition.LastTransitionTime == nil {
			condition.LastTransitionTime = &now
		}
		eci := findConditionIndexByType(status.Conditions, condition.Type)
		if eci >= 0 && status.Conditions[eci].Message == condition.Message {
			// If we already have a condition of this type, only update the timestamp if something
			// has changed.
			appConditions = append(appConditions, status.Conditions[eci])
		} else {
			// Otherwise we use the new incoming condition with an updated timestamp:
			appConditions = append(appConditions, condition)
		}
	}
	sort.Slice(appConditions, func(i, j int) bool {
		left := appConditions[i]
		right := appConditions[j]
		return fmt.Sprintf("%s/%s/%v", left.Type, left.Message, left.LastTransitionTime) < fmt.Sprintf("%s/%s/%v", right.Type, right.Message, right.LastTransitionTime)
	})
	status.Conditions = appConditions
}

func findConditionIndexByType(conditions []ApplicationCondition, t ApplicationConditionType) int {
	for i := range conditions {
		if conditions[i].Type == t {
			return i
		}
	}
	return -1
}

// GetErrorConditions returns list of application error conditions
func (status *ApplicationStatus) GetConditions(conditionTypes map[ApplicationConditionType]bool) []ApplicationCondition {
	result := make([]ApplicationCondition, 0)
	for i := range status.Conditions {
		condition := status.Conditions[i]
		if ok := conditionTypes[condition.Type]; ok {
			result = append(result, condition)
		}
	}
	return result
}

// IsError returns true if a condition indicates an error condition
func (condition *ApplicationCondition) IsError() bool {
	return strings.HasSuffix(condition.Type, "Error")
}

// Equals compares two instances of ApplicationSource and return true if instances are equal.
func (source *ApplicationSource) Equals(other *ApplicationSource) bool {
	if source == nil && other == nil {
		return true
	}
	if source == nil || other == nil {
		return false
	}
	if !source.Plugin.Equals(other.Plugin) {
		return false
	}
	// reflect.DeepEqual works fine for the other fields. Since the plugin fields are equal, set them to null so they're
	// not considered in the DeepEqual comparison.
	sourceCopy := source.DeepCopy()
	otherCopy := other.DeepCopy()
	sourceCopy.Plugin = nil
	otherCopy.Plugin = nil
	return reflect.DeepEqual(sourceCopy, otherCopy)
}

// ExplicitType returns the type (e.g. Helm, Kustomize, etc) of the application. If either none or multiple types are defined, returns an error.
func (source *ApplicationSource) ExplicitType() (*ApplicationSourceType, error) {
	var appTypes []ApplicationSourceType
	if source.Kustomize != nil {
		appTypes = append(appTypes, ApplicationSourceTypeKustomize)
	}
	if source.Helm != nil {
		appTypes = append(appTypes, ApplicationSourceTypeHelm)
	}
	if source.Directory != nil {
		appTypes = append(appTypes, ApplicationSourceTypeDirectory)
	}
	if source.Plugin != nil {
		appTypes = append(appTypes, ApplicationSourceTypePlugin)
	}
	if len(appTypes) == 0 {
		return nil, nil
	}
	if len(appTypes) > 1 {
		typeNames := make([]string, len(appTypes))
		for i := range appTypes {
			typeNames[i] = string(appTypes[i])
		}
		return nil, fmt.Errorf("multiple application sources defined: %s", strings.Join(typeNames, ","))
	}
	appType := appTypes[0]
	return &appType, nil
}

// GetProject returns the application's project. This is preferred over spec.Project which may be empty
func (spec ApplicationSpec) GetProject() string {
	if spec.Project == "" {
		return DefaultAppProjectName
	}
	return spec.Project
}

// GetRevisionHistoryLimit returns the currently set revision history limit for an application
func (spec ApplicationSpec) GetRevisionHistoryLimit() int {
	if spec.RevisionHistoryLimit != nil {
		return int(*spec.RevisionHistoryLimit)
	}
	return RevisionHistoryLimit
}

// getFinalizerIndex returns finalizer index in the list of object finalizers or -1 if finalizer does not exist
func getFinalizerIndex(meta metav1.ObjectMeta, name string) int {
	for i, finalizer := range meta.Finalizers {
		if finalizer == name {
			return i
		}
	}
	return -1
}

// setFinalizer adds or removes finalizer with the specified name
func setFinalizer(meta *metav1.ObjectMeta, name string, exist bool) {
	index := getFinalizerIndex(*meta, name)
	if exist != (index > -1) {
		if index > -1 {
			meta.Finalizers[index] = meta.Finalizers[len(meta.Finalizers)-1]
			meta.Finalizers = meta.Finalizers[:len(meta.Finalizers)-1]
		} else {
			meta.Finalizers = append(meta.Finalizers, name)
		}
	}
}

// ParseProxyUrl returns a parsed url and verifies that schema is correct
func ParseProxyUrl(proxyUrl string) (*url.URL, error) { //nolint:revive //FIXME(var-naming)
	u, err := url.Parse(proxyUrl)
	if err != nil {
		return nil, err
	}
	switch u.Scheme {
	case "http", "https", "socks5":
	default:
		return nil, fmt.Errorf("Failed to parse proxy url, unsupported scheme %q, must be http, https, or socks5", u.Scheme)
	}
	return u, nil
}

// UnmarshalToUnstructured unmarshals a resource representation in JSON to unstructured data
func UnmarshalToUnstructured(resource string) (*unstructured.Unstructured, error) {
	if resource == "" || resource == "null" {
		return nil, nil
	}
	var obj unstructured.Unstructured
	err := json.Unmarshal([]byte(resource), &obj)
	if err != nil {
		return nil, err
	}
	return &obj, nil
}

// LiveObject returns the live object representation of the resource by unmarshalling the
// `LiveState` field into an unstructured.Unstructured object. This object represents the current
// live state of the resource in the cluster.
func (r ResourceDiff) LiveObject() (*unstructured.Unstructured, error) {
	return UnmarshalToUnstructured(r.LiveState)
}

// TargetObject returns the target object representation of the resource by unmarshalling the
// `TargetState` field into an unstructured.Unstructured object. This object represents the desired
// state of the resource, as defined in the target configuration.
func (r ResourceDiff) TargetObject() (*unstructured.Unstructured, error) {
	return UnmarshalToUnstructured(r.TargetState)
}

// MarshalJSON marshals an application destination to JSON format
func (d *ApplicationDestination) MarshalJSON() ([]byte, error) {
	type Alias ApplicationDestination
	dest := d

	return json.Marshal(&struct{ *Alias }{Alias: (*Alias)(dest)})
}

// InstanceName returns the name of the application as used in the instance
// tracking values, i.e. in the format <namespace>_<name>. When the namespace
// of the application is similar to the value of defaultNs, only the name of
// the application is returned to keep backwards compatibility.
func (app *Application) InstanceName(defaultNs string) string {
	// When app has no namespace set, or the namespace is the default ns, we
	// return just the application name
	if app.Namespace == "" || app.Namespace == defaultNs {
		return app.Name
	}
	return app.Namespace + "_" + app.Name
}

// QualifiedName returns the full qualified name of the application, including
// the name of the namespace it is created in delimited by a forward slash,
// i.e. <namespace>/<appname>
func (app *Application) QualifiedName() string {
	if app.Namespace == "" {
		return app.Name
	}
	return app.Namespace + "/" + app.Name
}

// GetAnnotation returns the value of the specified annotation if it exists,
// e.g., a.GetAnnotation("argocd.argoproj.io/manifest-generate-paths").
// If the annotation does not exist, it returns an empty string.
func (app *Application) GetAnnotation(annotation string) string {
	v, exists := app.Annotations[annotation]
	if !exists {
		return ""
	}

	return v
}

// IsDeletionConfirmed checks whether the application has been approved for deletion.
// It compares the timestamp stored in the `AnnotationDeletionApproved` annotation
// with the provided 'since' time. If the annotation is missing or has an invalid
// timestamp format, it returns false.
func (app *Application) IsDeletionConfirmed(since time.Time) bool {
	val := app.GetAnnotation(AnnotationDeletionApproved)
	if val == "" {
		return false
	}
	parsedVal, err := time.Parse(time.RFC3339, val)
	if err != nil {
		return false
	}
	return parsedVal.After(since) || parsedVal.Equal(since)
}
