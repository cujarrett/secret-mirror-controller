/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	platformv1alpha1 "github.com/cujarrett/secret-mirror-controller/api/v1alpha1"
)

// The fake client stands in for the API server. These tests never start a
// manager, so they exercise Reconcile directly and run in milliseconds.

const (
	srcNS      = "mirror-src"
	dstNS      = "mirror-dst"
	otherNS    = "mirror-other"
	secretName = "hello"
	labelKey   = "mirror-test"
	dataKey    = "greeting"
	labelValue = "true"
)

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := scheme.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := platformv1alpha1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	return s
}

func namespace(name string, labels map[string]string) *corev1.Namespace {
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels}}
}

func mirror() *platformv1alpha1.SecretMirror {
	return &platformv1alpha1.SecretMirror{
		ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: srcNS},
		Spec: platformv1alpha1.SecretMirrorSpec{
			SourceSecret: secretName,
			TargetNamespaceSelector: metav1.LabelSelector{
				MatchLabels: map[string]string{labelKey: labelValue},
			},
		},
	}
}

func sourceSecret() *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: srcNS},
		Data:       map[string][]byte{dataKey: []byte("hi")},
	}
}

func reconcileOnce(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	s := testScheme(t)
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(objs...).
		WithStatusSubresource(&platformv1alpha1.SecretMirror{}).
		Build()

	r := &SecretMirrorReconciler{Client: c, Scheme: s, Recorder: events.NewFakeRecorder(20)}
	req := ctrl.Request{NamespacedName: client.ObjectKey{Namespace: srcNS, Name: secretName}}
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	return c
}

// getCopy reads the mirrored Secret from the target namespace, failing the test
// if it is absent - every caller treats a missing copy as the failure itself.
func getCopy(t *testing.T, c client.Client) *corev1.Secret {
	t.Helper()
	var s corev1.Secret
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: dstNS, Name: secretName}, &s); err != nil {
		t.Fatalf("get secret %s/%s: %v", dstNS, secretName, err)
	}
	return &s
}

func TestCreatesLabelledCopyInSelectedNamespace(t *testing.T) {
	c := reconcileOnce(t,
		namespace(srcNS, nil),
		namespace(dstNS, map[string]string{labelKey: labelValue}),
		sourceSecret(),
		mirror(),
	)

	copied := getCopy(t, c)
	if string(copied.Data[dataKey]) != "hi" {
		t.Errorf("data = %q, want hi", copied.Data[dataKey])
	}
	if got := copied.Labels[ownerLabel]; got != srcNS+".hello" {
		t.Errorf("owner label = %q, want %q", got, srcNS+".hello")
	}
}

func TestSkipsNamespaceThatDoesNotMatchSelector(t *testing.T) {
	c := reconcileOnce(t,
		namespace(srcNS, nil),
		namespace(otherNS, nil),
		sourceSecret(),
		mirror(),
	)

	err := c.Get(context.Background(), client.ObjectKey{Namespace: otherNS, Name: secretName}, &corev1.Secret{})
	if !apierrors.IsNotFound(err) {
		t.Errorf("err = %v, want NotFound", err)
	}
}

func TestNeverOverwritesASecretItDoesNotOwn(t *testing.T) {
	theirs := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: dstNS},
		Data:       map[string][]byte{"mine": []byte("donottouch")},
	}

	c := reconcileOnce(t,
		namespace(srcNS, nil),
		namespace(dstNS, map[string]string{labelKey: labelValue}),
		sourceSecret(),
		theirs,
		mirror(),
	)

	after := getCopy(t, c)
	if string(after.Data["mine"]) != "donottouch" {
		t.Errorf("data = %q, want donottouch", after.Data["mine"])
	}
}

func TestCorrectsDriftedCopy(t *testing.T) {
	drifted := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: dstNS,
			Labels:    map[string]string{ownerLabel: srcNS + ".hello"},
		},
		Data: map[string][]byte{dataKey: []byte("stale")},
	}

	c := reconcileOnce(t,
		namespace(srcNS, nil),
		namespace(dstNS, map[string]string{labelKey: labelValue}),
		sourceSecret(),
		drifted,
		mirror(),
	)

	if got := string(getCopy(t, c).Data[dataKey]); got != "hi" {
		t.Errorf("data = %q, want hi", got)
	}
}

func TestPrunesCopyFromNamespaceThatStoppedMatching(t *testing.T) {
	stale := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: otherNS,
			Labels:    map[string]string{ownerLabel: srcNS + ".hello"},
		},
		Data: map[string][]byte{dataKey: []byte("hi")},
	}

	c := reconcileOnce(t,
		namespace(srcNS, nil),
		namespace(otherNS, nil),
		sourceSecret(),
		stale,
		mirror(),
	)

	err := c.Get(context.Background(), client.ObjectKey{Namespace: otherNS, Name: secretName}, &corev1.Secret{})
	if !apierrors.IsNotFound(err) {
		t.Errorf("err = %v, want NotFound", err)
	}
}

func TestMissingSourceReportsWithoutDeletingCopies(t *testing.T) {
	existing := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: dstNS,
			Labels:    map[string]string{ownerLabel: srcNS + ".hello"},
		},
		Data: map[string][]byte{dataKey: []byte("hi")},
	}

	c := reconcileOnce(t,
		namespace(srcNS, nil),
		namespace(dstNS, map[string]string{labelKey: labelValue}),
		existing,
		mirror(),
	)

	getCopy(t, c) // fatal if the copy was deleted

	var m platformv1alpha1.SecretMirror
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: srcNS, Name: secretName}, &m); err != nil {
		t.Fatal(err)
	}
	if len(m.Status.Conditions) == 0 || m.Status.Conditions[0].Reason != "SourceMissing" {
		t.Errorf("conditions = %+v, want reason SourceMissing", m.Status.Conditions)
	}
}

func TestDeletionRemovesCopiesAndReleasesFinalizer(t *testing.T) {
	now := metav1.Now()
	deleting := mirror()
	deleting.DeletionTimestamp = &now
	deleting.Finalizers = []string{finalizerName}

	copyInDst := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: dstNS,
			Labels:    map[string]string{ownerLabel: srcNS + ".hello"},
		},
		Data: map[string][]byte{dataKey: []byte("hi")},
	}

	c := reconcileOnce(t,
		namespace(srcNS, nil),
		namespace(dstNS, map[string]string{labelKey: labelValue}),
		sourceSecret(),
		copyInDst,
		deleting,
	)

	err := c.Get(context.Background(), client.ObjectKey{Namespace: dstNS, Name: secretName}, &corev1.Secret{})
	if !apierrors.IsNotFound(err) {
		t.Errorf("copy err = %v, want NotFound", err)
	}

	// The fake client deletes an object once its last finalizer is removed.
	var m platformv1alpha1.SecretMirror
	err = c.Get(context.Background(), client.ObjectKey{Namespace: srcNS, Name: secretName}, &m)
	if err == nil && len(m.Finalizers) > 0 {
		t.Errorf("finalizers = %v, want none", m.Finalizers)
	}
}
