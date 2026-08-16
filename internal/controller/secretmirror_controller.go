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
	"fmt"
	"maps"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	platformv1alpha1 "github.com/cujarrett/secret-mirror-controller/api/v1alpha1"
)

// ownerLabel marks a Secret as a copy this controller made. Anything at the
// target name without it belongs to someone else and is never touched.
const ownerLabel = "platform.local.lab/mirrored-by"

// The mirror is the only record of which Secret was copied where, so deleting
// it outright would strand every copy. This finalizer holds the object in the
// API until the copies are gone. The usual answer, ownerReferences, does not
// work here - the garbage collector ignores an owner in a different namespace.
const finalizerName = "platform.local.lab/cleanup-copies"

// ownerValue identifies which SecretMirror made the copy. Label values cannot
// contain a slash, so namespace and name are joined with a dot.
func ownerValue(mirror *platformv1alpha1.SecretMirror) string {
	return mirror.Namespace + "." + mirror.Name
}

// SecretMirrorReconciler reconciles a SecretMirror object
type SecretMirrorReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder events.EventRecorder
}

// +kubebuilder:rbac:groups=platform.local.lab,resources=secretmirrors,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=platform.local.lab,resources=secretmirrors/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=platform.local.lab,resources=secretmirrors/finalizers,verbs=update
// Secret access is granted per namespace, never cluster-wide - a controller that
// can write a Secret anywhere can plant a fake TLS cert or pull credential in any
// namespace. controller-gen turns each marker into a Role in that namespace.
//
// Target namespaces are named by guests at creation time, so they cannot be
// listed in a marker. The rule below generates a ClusterRole that is never bound
// cluster-wide - launchpad-api renders a RoleBinding beside each sandbox
// namespace, so the grant appears with the sandbox and disappears with it.
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// Reconcile makes every selected namespace hold a copy of the source Secret.
// It runs start to finish on every trigger and never assumes what changed.
func (r *SecretMirrorReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var mirror platformv1alpha1.SecretMirror
	if err := r.Get(ctx, req.NamespacedName, &mirror); err != nil {
		// Already deleted - nothing to do. Step 6 gives this a real branch.
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Marked for deletion. Remove every copy, then release the finalizer so the
	// API server can finish deleting the mirror itself.
	if !mirror.DeletionTimestamp.IsZero() {
		if err := r.pruneCopies(ctx, &mirror, nil); err != nil {
			return ctrl.Result{}, err
		}
		controllerutil.RemoveFinalizer(&mirror, finalizerName)
		return ctrl.Result{}, r.Update(ctx, &mirror)
	}

	if controllerutil.AddFinalizer(&mirror, finalizerName) {
		if err := r.Update(ctx, &mirror); err != nil {
			return ctrl.Result{}, err
		}
	}

	// A missing source is reported, never acted on. Deleting copies here would
	// turn a typo or a not-yet-created Secret into an outage in every sandbox.
	var source corev1.Secret
	sourceKey := client.ObjectKey{Namespace: mirror.Namespace, Name: mirror.Spec.SourceSecret}
	if err := r.Get(ctx, sourceKey, &source); err != nil {
		if apierrors.IsNotFound(err) {
			r.Recorder.Eventf(&mirror, nil, corev1.EventTypeWarning, "SourceMissing", "Mirror",
				"Secret %s not found - existing copies left untouched", sourceKey)
			return ctrl.Result{}, r.setStatus(ctx, &mirror, mirror.Status.Copies, metav1.Condition{
				Type:    "Ready",
				Status:  metav1.ConditionFalse,
				Reason:  "SourceMissing",
				Message: fmt.Sprintf("Secret %s does not exist", sourceKey),
			})
		}
		return ctrl.Result{}, fmt.Errorf("read source secret %s: %w", sourceKey, err)
	}

	targets, err := r.selectedNamespaces(ctx, &mirror)
	if err != nil {
		return ctrl.Result{}, err
	}

	copies, conflicts := 0, []string{}
	for _, ns := range targets {
		ok, err := r.mirrorInto(ctx, &mirror, &source, ns)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("mirror into %s: %w", ns, err)
		}
		if ok {
			copies++
			continue
		}
		conflicts = append(conflicts, ns)
		r.Recorder.Eventf(&mirror, nil, corev1.EventTypeWarning, "NotOwned", "Mirror",
			"Secret %s/%s exists and was not created by this mirror - left untouched", ns, source.Name)
	}

	if err := r.pruneCopies(ctx, &mirror, targets); err != nil {
		return ctrl.Result{}, err
	}

	ready := metav1.Condition{Type: "Ready", Status: metav1.ConditionTrue, Reason: "InSync",
		Message: fmt.Sprintf("%d of %d selected namespaces hold a copy", copies, len(targets))}
	if len(conflicts) > 0 {
		ready.Status = metav1.ConditionFalse
		ready.Reason = "ConflictingSecrets"
		ready.Message = fmt.Sprintf("not owned by this mirror in %v", conflicts)
	}

	log.Info("reconciled", "source", mirror.Spec.SourceSecret, "copies", copies, "conflicts", len(conflicts))

	if err := r.setStatus(ctx, &mirror, int32(copies), ready); err != nil {
		return ctrl.Result{}, err
	}

	// Copies live in namespaces nothing watches, so a copy deleted by hand is
	// only noticed on this pass. Sandbox creation and source renewal still
	// arrive as watch events and do not wait for it.
	return ctrl.Result{RequeueAfter: 10 * time.Minute}, nil
}

// selectedNamespaces returns the namespaces matching the mirror's selector,
// minus its own - copying a Secret over itself is never wanted.
func (r *SecretMirrorReconciler) selectedNamespaces(ctx context.Context, mirror *platformv1alpha1.SecretMirror) ([]string, error) {
	selector, err := metav1.LabelSelectorAsSelector(&mirror.Spec.TargetNamespaceSelector)
	if err != nil {
		return nil, fmt.Errorf("bad targetNamespaceSelector: %w", err)
	}

	var list corev1.NamespaceList
	if err := r.List(ctx, &list, client.MatchingLabelsSelector{Selector: selector}); err != nil {
		return nil, fmt.Errorf("list namespaces: %w", err)
	}

	names := make([]string, 0, len(list.Items))
	for _, ns := range list.Items {
		if ns.Name == mirror.Namespace {
			continue
		}
		names = append(names, ns.Name)
	}
	return names, nil
}

// mirrorInto makes one namespace hold a copy of the source: create it if it is
// missing, correct it if it has drifted, leave it alone if it already matches.
// A Secret at the target name that this mirror does not own is never modified.
// It reports false when a Secret it does not own is in the way.
func (r *SecretMirrorReconciler) mirrorInto(ctx context.Context, mirror *platformv1alpha1.SecretMirror, source *corev1.Secret, namespace string) (bool, error) {
	log := logf.FromContext(ctx)

	var existing corev1.Secret
	err := r.Get(ctx, client.ObjectKey{Namespace: namespace, Name: source.Name}, &existing)
	if apierrors.IsNotFound(err) {
		dup := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      source.Name,
				Namespace: namespace,
				Labels:    map[string]string{ownerLabel: ownerValue(mirror)},
			},
			Type: source.Type,
			Data: maps.Clone(source.Data),
		}
		log.Info("creating copy", "namespace", namespace)
		return true, r.Create(ctx, dup)
	}
	if err != nil {
		return false, err
	}

	// Someone else's Secret is sitting at this name. Report it and move on -
	// a conflict is never resolved by overwriting.
	if existing.Labels[ownerLabel] != ownerValue(mirror) {
		log.Info("refusing to overwrite a Secret this mirror does not own",
			"namespace", namespace, "name", source.Name, "owner", existing.Labels[ownerLabel])
		return false, nil
	}

	// Only the data is compared. A Secret's type is immutable, so a mismatch
	// there needs a delete and recreate - not worth handling until it happens.
	if maps.EqualFunc(existing.Data, source.Data, bytesEqual) {
		return true, nil
	}

	existing.Data = maps.Clone(source.Data)
	log.Info("correcting drifted copy", "namespace", namespace)
	return true, r.Update(ctx, &existing)
}

// setStatus records what this reconcile found. observedGeneration is what lets a
// reader tell whether the rest of the status describes the spec they are seeing.
func (r *SecretMirrorReconciler) setStatus(ctx context.Context, mirror *platformv1alpha1.SecretMirror, copies int32, cond metav1.Condition) error {
	mirror.Status.Copies = copies
	mirror.Status.ObservedGeneration = mirror.Generation
	cond.ObservedGeneration = mirror.Generation
	meta.SetStatusCondition(&mirror.Status.Conditions, cond)
	return r.Status().Update(ctx, mirror)
}

// pruneCopies removes copies from namespaces the selector no longer matches.
// Passing a nil target list removes every copy, which is what deletion needs.
// Namespaces are listed unfiltered and the selected ones subtracted, because a
// namespace that stopped matching cannot be found by the selector any more.
func (r *SecretMirrorReconciler) pruneCopies(ctx context.Context, mirror *platformv1alpha1.SecretMirror, targets []string) error {
	log := logf.FromContext(ctx)

	// A set, so the membership test below is a lookup rather than a scan of
	// targets for every namespace in the cluster.
	selected := make(map[string]bool, len(targets))
	for _, ns := range targets {
		selected[ns] = true
	}

	// Unfiltered on purpose - see the doc comment.
	var all corev1.NamespaceList
	if err := r.List(ctx, &all); err != nil {
		return fmt.Errorf("list namespaces: %w", err)
	}

	// Everything below narrows the field one rule at a time. Whatever survives
	// all four skips is a stale copy this mirror is responsible for.
	for _, ns := range all.Items {
		// Wanted here, or it is the source itself.
		if selected[ns.Name] || ns.Name == mirror.Namespace {
			continue
		}

		// Most namespaces have no Secret by this name, so NotFound here is the
		// expected answer rather than something to retry.
		var stale corev1.Secret
		err := r.Get(ctx, client.ObjectKey{Namespace: ns.Name, Name: mirror.Spec.SourceSecret}, &stale)
		if apierrors.IsNotFound(err) {
			continue
		}
		if err != nil {
			return err
		}

		// Only ever delete a copy this mirror made. Rule 1 applies to deletes
		// just as much as to writes.
		if stale.Labels[ownerLabel] != ownerValue(mirror) {
			continue
		}

		log.Info("pruning copy from unselected namespace", "namespace", ns.Name)
		if err := r.Delete(ctx, &stale); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("prune from %s: %w", ns.Name, err)
		}
	}
	return nil
}

func bytesEqual(a, b []byte) bool {
	return string(a) == string(b)
}

// mirrorsForSecret maps a Secret event back to the mirrors that care about it -
// the ones sourcing from it, plus the one that owns it if it is a copy.
func (r *SecretMirrorReconciler) mirrorsForSecret(ctx context.Context, obj client.Object) []ctrl.Request {
	var mirrors platformv1alpha1.SecretMirrorList
	if err := r.List(ctx, &mirrors); err != nil {
		return nil
	}

	var requests []ctrl.Request
	for _, m := range mirrors.Items {
		isSource := m.Namespace == obj.GetNamespace() && m.Spec.SourceSecret == obj.GetName()
		isOurCopy := obj.GetLabels()[ownerLabel] == m.Namespace+"."+m.Name
		if isSource || isOurCopy {
			requests = append(requests, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(&m)})
		}
	}
	return requests
}

// mirrorsForNamespace enqueues every mirror on any namespace event. A namespace
// that just lost its label no longer matches any selector, so filtering by
// selector here would drop exactly the event that triggers a prune.
func (r *SecretMirrorReconciler) mirrorsForNamespace(ctx context.Context, _ client.Object) []ctrl.Request {
	var mirrors platformv1alpha1.SecretMirrorList
	if err := r.List(ctx, &mirrors); err != nil {
		return nil
	}

	requests := make([]ctrl.Request, 0, len(mirrors.Items))
	for _, m := range mirrors.Items {
		requests = append(requests, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(&m)})
	}
	return requests
}

// SetupWithManager sets up the controller with the Manager.
func (r *SecretMirrorReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&platformv1alpha1.SecretMirror{}).
		Watches(&corev1.Secret{}, handler.EnqueueRequestsFromMapFunc(r.mirrorsForSecret)).
		Watches(&corev1.Namespace{}, handler.EnqueueRequestsFromMapFunc(r.mirrorsForNamespace)).
		Named("secretmirror").
		Complete(r)
}
