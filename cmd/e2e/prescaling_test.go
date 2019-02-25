package main

import (
	"fmt"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestPrescalingWithoutHPA(t *testing.T) {
	t.Parallel()
	stacksetName := "stackset-prescale-no-hpa"
	specFactory := NewTestStacksetSpecFactory(stacksetName).Ingress().StackGC(3, 0).Replicas(3)

	// create stack with 3 replicas
	firstStack := "v1"
	spec := specFactory.Create(firstStack)
	err := createStackSet(stacksetName, 1, spec)
	require.NoError(t, err)
	_, err = waitForStack(t, stacksetName, firstStack)
	require.NoError(t, err)

	// create second stack with 3 replicas
	secondStack := "v2"
	spec = specFactory.Create(secondStack)
	err = updateStackset(stacksetName, spec)
	require.NoError(t, err)
	_, err = waitForStack(t, stacksetName, secondStack)
	require.NoError(t, err)

	// switch traffic so that both stacks are receiving equal traffic and verify traffic has actually switched
	fullFirstStack := fmt.Sprintf("%s-%s", stacksetName, firstStack)
	fullSecondStack := fmt.Sprintf("%s-%s", stacksetName, secondStack)
	_, err = waitForIngress(t, stacksetName)
	require.NoError(t, err)
	desiredTraffic := map[string]float64{
		fullFirstStack:  50,
		fullSecondStack: 50,
	}
	err = setDesiredTrafficWeights(stacksetName, desiredTraffic)
	require.NoError(t, err)
	err = trafficWeightsUpdated(t, stacksetName, weightKindActual, desiredTraffic).withTimeout(time.Minute * 4).await()
	require.NoError(t, err)

	// create third stack with only 1 replica and wait for the deployment to be created
	thirdStack := "v3"
	fullThirdStack := fmt.Sprintf("%s-%s", stacksetName, thirdStack)
	spec = specFactory.Replicas(1).Create(thirdStack)
	err = updateStackset(stacksetName, spec)
	require.NoError(t, err)
	deployment, err := waitForDeployment(t, fullThirdStack)
	require.NoError(t, err)
	require.EqualValues(t, 1, *deployment.Spec.Replicas)

	// finally switch 1/10 of the traffic to the new stack
	desiredTraffic = map[string]float64{
		fullThirdStack:  10,
		fullFirstStack:  40,
		fullSecondStack: 50,
	}
	err = setDesiredTrafficWeights(stacksetName, desiredTraffic)
	require.NoError(t, err)
	err = trafficWeightsUpdated(t, stacksetName, weightKindActual, desiredTraffic).withTimeout(time.Minute * 4).await()
	require.NoError(t, err)

	// recheck the deployment of the last stack and verify that the number of replicas is the sum of the previous stacks
	// till the end of the prescaling timeout
	for i := 1; i <= 6; i++ {
		deployment, err = waitForDeployment(t, fullThirdStack)
		require.NoError(t, err)
		require.EqualValues(t, 6, *(deployment.Spec.Replicas))
		time.Sleep(time.Second * 10)
	}
	time.Sleep(time.Second * 10)
	deployment, err = waitForDeployment(t, fullThirdStack)
	require.EqualValues(t, 1, *(deployment.Spec.Replicas))
}

func TestPrescalingWithHPA(t *testing.T) {
	t.Parallel()
	stacksetName := "stackset-prescale-hpa"
	specFactory := NewTestStacksetSpecFactory(stacksetName).Ingress().StackGC(3, 0).
		HPA(1, 10).Replicas(3)

	// create first stack with 3 replicas
	firstStack := "v1"
	spec := specFactory.Create(firstStack)
	err := createStackSet(stacksetName, 1, spec)
	require.NoError(t, err)
	_, err = waitForStack(t, stacksetName, firstStack)
	require.NoError(t, err)

	// create second stack with 3 replicas
	secondStack := "v2"
	spec = specFactory.Create(secondStack)
	err = updateStackset(stacksetName, spec)
	require.NoError(t, err)
	_, err = waitForStack(t, stacksetName, secondStack)
	require.NoError(t, err)

	// switch traffic so that both stacks are receiving equal traffic
	fullFirstStack := fmt.Sprintf("%s-%s", stacksetName, firstStack)
	fullSecondStack := fmt.Sprintf("%s-%s", stacksetName, secondStack)
	_, err = waitForIngress(t, stacksetName)
	require.NoError(t, err)
	desiredTraffic := map[string]float64{
		fullFirstStack:  50,
		fullSecondStack: 50,
	}
	err = setDesiredTrafficWeights(stacksetName, desiredTraffic)
	require.NoError(t, err)
	err = trafficWeightsUpdated(t, stacksetName, weightKindActual, desiredTraffic).withTimeout(time.Minute * 4).await()
	require.NoError(t, err)

	// create a third stack with only one replica and verify the deployment has only one pod
	thirdStack := "v3"
	fullThirdStack := fmt.Sprintf("%s-%s", stacksetName, thirdStack)
	spec = specFactory.Replicas(1).Create(thirdStack)
	err = updateStackset(stacksetName, spec)
	require.NoError(t, err)
	deployment, err := waitForDeployment(t, fullThirdStack)
	require.NoError(t, err)
	require.EqualValues(t, 1, *deployment.Spec.Replicas)

	// switch 1/10 of the traffic to the third stack and wait for the process to be complete
	desiredTraffic = map[string]float64{
		fullThirdStack:  10,
		fullFirstStack:  40,
		fullSecondStack: 50,
	}

	err = setDesiredTrafficWeights(stacksetName, desiredTraffic)
	require.NoError(t, err)
	err = trafficWeightsUpdated(t, stacksetName, weightKindActual, desiredTraffic).withTimeout(time.Minute * 4).await()
	require.NoError(t, err)

	// verify that the third stack now has 6 replicas till the end of the prescaling period
	for i := 1; i <= 6; i++ {
		hpa, err := waitForHPA(t, fullThirdStack)
		require.NoError(t, err)
		require.EqualValues(t, 6, *(hpa.Spec.MinReplicas))
		time.Sleep(time.Second * 10)
	}
	time.Sleep(time.Second * 10)
	hpa, err := waitForHPA(t, fullThirdStack)
	require.NoError(t, err)
	require.EqualValues(t, 1, *(hpa.Spec.MinReplicas))
}

func TestPrescalingPreventDelete(t *testing.T) {
	stackPrescalingTimeout := 5
	t.Parallel()
	stacksetName := "stackset-prevent-delete"
	factory := NewTestStacksetSpecFactory(stacksetName).StackGC(1, 0).Ingress().Replicas(3)

	// create stackset with first version
	firstVersion := "v1"
	fullFirstStack := fmt.Sprintf("%s-%s", stacksetName, firstVersion)
	firstCreateTimestamp := time.Now()
	err := createStackSet(stacksetName, stackPrescalingTimeout, factory.Create(firstVersion))
	require.NoError(t, err)
	_, err = waitForDeployment(t, fullFirstStack)
	require.NoError(t, err)
	_, err = waitForIngress(t, fullFirstStack)
	require.NoError(t, err)

	// update stackset with second version
	secondVersion := "v2"
	fullSecondStack := fmt.Sprintf("%s-%s", stacksetName, firstVersion)
	secondCreateTimestamp := time.Now()
	err = updateStackset(stacksetName, factory.Create(secondVersion))
	require.NoError(t, err)
	_, err = waitForDeployment(t, fullSecondStack)
	require.NoError(t, err)
	_, err = waitForIngress(t, fullSecondStack)
	require.NoError(t, err)

	// switch all traffic to the new stack
	desiredTrafficMap := map[string]float64{
		fullSecondStack: 100,
	}
	err = setDesiredTrafficWeights(stacksetName, desiredTrafficMap)
	require.NoError(t, err)
	err = trafficWeightsUpdated(t, stacksetName, weightKindActual, desiredTrafficMap).withTimeout(2 * time.Minute).await()
	require.NoError(t, err)

	// update stackset with third version
	thirdVersion := "v3"
	fullThirdStack := fmt.Sprintf("%s-%s", stacksetName, firstVersion)
	thirdCreateTimestamp := time.Now()
	err = updateStackset(stacksetName, factory.Create(thirdVersion))
	require.NoError(t, err)
	_, err = waitForDeployment(t, fullThirdStack)
	require.NoError(t, err)
	_, err = waitForIngress(t, fullThirdStack)
	require.NoError(t, err)

	desiredTrafficMap = map[string]float64{
		fullThirdStack: 100,
	}
	err = setDesiredTrafficWeights(stacksetName, desiredTrafficMap)
	require.NoError(t, err)
	err = trafficWeightsUpdated(t, stacksetName, weightKindActual, desiredTrafficMap).withTimeout(2 * time.Minute).await()
	require.NoError(t, err)

	// verify that all stack deployments are still present and their prescaling is active
	for time.Now().Before(firstCreateTimestamp.Add(time.Minute * time.Duration(stackPrescalingTimeout))) {
		firstDeployment, err := waitForDeployment(t, fullFirstStack)
		require.NoError(t, err)
		require.EqualValues(t, 3, *firstDeployment.Spec.Replicas)
		time.Sleep(15 * time.Second)
	}
	for time.Now().Before(secondCreateTimestamp.Add(time.Minute * time.Duration(stackPrescalingTimeout))) {
		secondDeployment, err := waitForDeployment(t, fullSecondStack)
		require.NoError(t, err)
		require.EqualValues(t, 3, *secondDeployment.Spec.Replicas)
		time.Sleep(15 * time.Second)
	}

	for time.Now().Before(thirdCreateTimestamp.Add(time.Minute * time.Duration(stackPrescalingTimeout))) {
		thirdDeployment, err := waitForDeployment(t, fullThirdStack)
		require.NoError(t, err)
		require.EqualValues(t, 3, *thirdDeployment.Spec.Replicas)
		time.Sleep(15 * time.Second)
	}
}