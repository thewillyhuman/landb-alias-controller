package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestHasLandbMetadata_MatchesLabelsAndAnnotations(t *testing.T) {
	assert.True(t, hasLandbMetadata(&corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{"landb.cern.ch/set": "set-a"},
		},
	}))
	assert.True(t, hasLandbMetadata(&corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{"landb.cern.ch/owner": "team-a"},
		},
	}))
}

func TestHasLandbMetadata_IgnoresOtherKeys(t *testing.T) {
	assert.False(t, hasLandbMetadata(&corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Labels:      map[string]string{"example.com/set": "set-a"},
			Annotations: map[string]string{"example.com/owner": "team-a"},
		},
	}))
}

func TestLandbMetadataChanged_DetectsLabelAndAnnotationChanges(t *testing.T) {
	oldNode := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Labels:      map[string]string{"landb.cern.ch/set": "set-a"},
			Annotations: map[string]string{"landb.cern.ch/owner": "team-a"},
		},
	}
	newNode := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Labels:      map[string]string{"landb.cern.ch/set": "set-b"},
			Annotations: map[string]string{"landb.cern.ch/owner": "team-a"},
		},
	}

	assert.True(t, landbMetadataChanged(oldNode, newNode))

	newNode.Labels["landb.cern.ch/set"] = "set-a"
	delete(newNode.Annotations, "landb.cern.ch/owner")

	assert.True(t, landbMetadataChanged(oldNode, newNode))
}

func TestLandbMetadataChanged_IgnoresUnrelatedChanges(t *testing.T) {
	oldNode := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Labels:      map[string]string{"landb.cern.ch/set": "set-a", "other": "old"},
			Annotations: map[string]string{"other": "old"},
		},
	}
	newNode := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Labels:      map[string]string{"landb.cern.ch/set": "set-a", "other": "new"},
			Annotations: map[string]string{"other": "new"},
		},
	}

	assert.False(t, landbMetadataChanged(oldNode, newNode))
}

func TestPrefixedMapChanged_DetectsEmptyValueAddition(t *testing.T) {
	assert.True(t, prefixedMapChanged(nil, map[string]string{"landb.cern.ch/set": ""}, landbMetadataPrefix))
	assert.True(t, prefixedMapChanged(map[string]string{"landb.cern.ch/set": ""}, nil, landbMetadataPrefix))
}
