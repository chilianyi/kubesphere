package s2ibinary

import (
	fakeS3 "kubesphere.io/kubesphere/pkg/simple/client/s3/fake"
	"reflect"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/diff"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	core "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	s2i "kubesphere.io/kubesphere/pkg/apis/devops/v1alpha1"
	"kubesphere.io/kubesphere/pkg/client/clientset/versioned/fake"
	informers "kubesphere.io/kubesphere/pkg/client/informers/externalversions"
)

var (
	alwaysReady        = func() bool { return true }
	noResyncPeriodFunc = func() time.Duration { return 0 }
)

type fixture struct {
	t *testing.T

	client     *fake.Clientset
	kubeclient *k8sfake.Clientset
	// Objects to put in the store.
	s2ibinaryLister []*s2i.S2iBinary
	actions         []core.Action
	// Objects from here preloaded into NewSimpleFake.
	objects []runtime.Object
	// Objects from here preloaded into s3
	initS3Objects   []*fakeS3.Object
	expectS3Objects []*fakeS3.Object
}

func newFixture(t *testing.T) *fixture {
	f := &fixture{}
	f.t = t
	f.objects = []runtime.Object{}
	return f
}

func newS2iBinary(name string, spec s2i.S2iBinarySpec) *s2i.S2iBinary {
	return &s2i.S2iBinary{
		TypeMeta: metav1.TypeMeta{APIVersion: s2i.SchemeGroupVersion.String()},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: metav1.NamespaceDefault,
		},
		Spec: s2i.S2iBinarySpec{},
	}
}
func newDeletingS2iBinary(name string) *s2i.S2iBinary {
	deleteTime := metav1.Now()
	return &s2i.S2iBinary{
		TypeMeta: metav1.TypeMeta{APIVersion: s2i.SchemeGroupVersion.String()},
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			Namespace:         metav1.NamespaceDefault,
			Finalizers:        []string{s2i.S2iBinaryFinalizerName},
			DeletionTimestamp: &deleteTime,
		},
	}
}

func (f *fixture) newController() (*Controller, informers.SharedInformerFactory, *fakeS3.FakeS3) {
	f.client = fake.NewSimpleClientset(f.objects...)
	f.kubeclient = k8sfake.NewSimpleClientset()

	i := informers.NewSharedInformerFactory(f.client, noResyncPeriodFunc())
	s3I := fakeS3.NewFakeS3(f.expectS3Objects...)

	c := NewController(f.kubeclient, f.client, i.Devops().V1alpha1().S2iBinaries(), s3I)

	c.s2iBinarySynced = alwaysReady
	c.eventRecorder = &record.FakeRecorder{}

	for _, f := range f.s2ibinaryLister {
		i.Devops().V1alpha1().S2iBinaries().Informer().GetIndexer().Add(f)
	}

	return c, i, s3I
}

func (f *fixture) run(fooName string) {
	f.runController(fooName, true, false)
}

func (f *fixture) runExpectError(fooName string) {
	f.runController(fooName, true, true)
}

func (f *fixture) runController(s2iBinaryName string, startInformers bool, expectError bool) {
	c, i, s3I := f.newController()
	if startInformers {
		stopCh := make(chan struct{})
		defer close(stopCh)
		i.Start(stopCh)
	}

	err := c.syncHandler(s2iBinaryName)
	if !expectError && err != nil {
		f.t.Errorf("error syncing foo: %v", err)
	} else if expectError && err == nil {
		f.t.Error("expected error syncing foo, got nil")
	}

	actions := filterInformerActions(f.client.Actions())
	for i, action := range actions {
		if len(f.actions) < i+1 {
			f.t.Errorf("%d unexpected actions: %+v", len(actions)-len(f.actions), actions[i:])
			break
		}

		expectedAction := f.actions[i]
		checkAction(expectedAction, action, f.t)
	}

	if len(f.actions) > len(actions) {
		f.t.Errorf("%d additional expected actions:%+v", len(f.actions)-len(actions), f.actions[len(actions):])
	}
	if len(s3I.Storage) != len(f.expectS3Objects) {
		f.t.Errorf(" unexpected objects: %v", s3I.Storage)
	}
}

// checkAction verifies that expected and actual actions are equal and both have
// same attached resources
func checkAction(expected, actual core.Action, t *testing.T) {
	if !(expected.Matches(actual.GetVerb(), actual.GetResource().Resource) && actual.GetSubresource() == expected.GetSubresource()) {
		t.Errorf("Expected\n\t%#v\ngot\n\t%#v", expected, actual)
		return
	}

	if reflect.TypeOf(actual) != reflect.TypeOf(expected) {
		t.Errorf("Action has wrong type. Expected: %t. Got: %t", expected, actual)
		return
	}

	switch a := actual.(type) {
	case core.CreateActionImpl:
		e, _ := expected.(core.CreateActionImpl)
		expObject := e.GetObject()
		object := a.GetObject()

		if !reflect.DeepEqual(expObject, object) {
			t.Errorf("Action %s %s has wrong object\nDiff:\n %s",
				a.GetVerb(), a.GetResource().Resource, diff.ObjectGoPrintSideBySide(expObject, object))
		}
	case core.UpdateActionImpl:
		e, _ := expected.(core.UpdateActionImpl)
		expObject := e.GetObject()
		object := a.GetObject()

		if !reflect.DeepEqual(expObject, object) {
			t.Errorf("Action %s %s has wrong object\nDiff:\n %s",
				a.GetVerb(), a.GetResource().Resource, diff.ObjectGoPrintSideBySide(expObject, object))
		}
	case core.PatchActionImpl:
		e, _ := expected.(core.PatchActionImpl)
		expPatch := e.GetPatch()
		patch := a.GetPatch()

		if !reflect.DeepEqual(expPatch, patch) {
			t.Errorf("Action %s %s has wrong patch\nDiff:\n %s",
				a.GetVerb(), a.GetResource().Resource, diff.ObjectGoPrintSideBySide(expPatch, patch))
		}
	default:
		t.Errorf("Uncaptured Action %s %s, you should explicitly add a case to capture it",
			actual.GetVerb(), actual.GetResource().Resource)
	}
}

// filterInformerActions filters list and watch actions for testing resources.
// Since list and watch don't change resource state we can filter it to lower
// nose level in our tests.
func filterInformerActions(actions []core.Action) []core.Action {
	ret := []core.Action{}
	for _, action := range actions {
		if len(action.GetNamespace()) == 0 &&
			(action.Matches("list", "foos") ||
				action.Matches("watch", "foos") ||
				action.Matches("list", "deployments") ||
				action.Matches("watch", "deployments")) {
			continue
		}
		ret = append(ret, action)
	}

	return ret
}

func (f *fixture) expectUpdateS2iBinaryAction(s2ibinary *s2i.S2iBinary) {
	action := core.NewUpdateAction(schema.GroupVersionResource{Resource: s2i.ResourcePluralS2iBinary}, s2ibinary.Namespace, s2ibinary)
	f.actions = append(f.actions, action)
}

func getKey(s2ibinary *s2i.S2iBinary, t *testing.T) string {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(s2ibinary)
	if err != nil {
		t.Errorf("Unexpected error getting key for s2ibinary %v: %v", s2ibinary.Name, err)
		return ""
	}
	return key
}

func TestDoNothing(t *testing.T) {
	f := newFixture(t)
	s2iBinary := newS2iBinary("test", s2i.S2iBinarySpec{})

	f.s2ibinaryLister = append(f.s2ibinaryLister, s2iBinary)
	f.objects = append(f.objects, s2iBinary)

	f.expectUpdateS2iBinaryAction(s2iBinary)
	f.run(getKey(s2iBinary, t))
}

func TestDeleteS3Object(t *testing.T) {
	f := newFixture(t)
	s2iBinary := newDeletingS2iBinary("test")

	f.s2ibinaryLister = append(f.s2ibinaryLister, s2iBinary)
	f.objects = append(f.objects, s2iBinary)
	f.initS3Objects = []*fakeS3.Object{&fakeS3.Object{
		Key: "default-test",
	}}
	f.expectS3Objects = []*fakeS3.Object{}
	f.expectUpdateS2iBinaryAction(s2iBinary)
	f.run(getKey(s2iBinary, t))

}
