/*
Copyright 2021.

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

package client

import (
	"fmt"

	"github.com/IBM/go-sdk-core/v5/core"
	"github.com/IBM/platform-services-go-sdk/resourcemanagerv2"
	"github.com/IBM/vpc-go-sdk/vpcv1"
	"github.com/golang-jwt/jwt"
	"github.com/openshift/machine-api-operator/pkg/controller/machine"
	ibmcloudproviderv1 "github.com/openshift/machine-api-provider-ibmcloud/pkg/apis/ibmcloudprovider/v1"
	"github.com/pkg/errors"
)

// instance not found error
var errInstanceNotFound = errors.New("instance not found")

// Client is a wrapper object for IBM SDK clients
type Client interface {
	// Instances functions
	InstanceGetByID(instanceID string) (*vpcv1.Instance, error)
	InstanceExistsByName(name string, machineProviderConfig *ibmcloudproviderv1.IBMCloudMachineProviderSpec) (bool, error)
	InstanceGetByName(name string, machineProviderConfig *ibmcloudproviderv1.IBMCloudMachineProviderSpec) (*vpcv1.Instance, error)
	InstanceDeleteByName(name string, machineProviderConfig *ibmcloudproviderv1.IBMCloudMachineProviderSpec) error
	InstanceCreate(machineName string, machineProviderConfig *ibmcloudproviderv1.IBMCloudMachineProviderSpec, userData string) (*vpcv1.Instance, error)
	InstanceGetProfile(profileName string) (bool, error)

	// Helper functions
	GetAccountID() (string, error)
	GetCustomImageByName(imageName string, resourceGroupID string) (string, error)
	VerifyInstanceProfile(profile string) (string, error)
	GetVPCIDByName(vpcName string, resourceGroupID string) (string, error)
	GetResourceGroupIDByName(resourceGroupName string) (string, error)
	GetSubnetIDbyName(subnetName string, resourceGroupID string) (string, error)
	GetSecurityGroupsByName(securityGroupNames []string, resourceGroupID string, vpcID string) ([]vpcv1.SecurityGroupIdentityIntf, error)
	GetDedicatedHostByName(dedicatedHostName string, resourceGroupID string, zoneName string) (string, error)
}

// ibmCloudClient makes call to IBM Cloud APIs
type ibmCloudClient struct {
	AccountID              string
	vpcService             *vpcv1.VpcV1
	resourceManagerService *resourcemanagerv2.ResourceManagerV2
}

// IbmcloudClientBuilderFuncType is function type for building ibm cloud client
type IbmcloudClientBuilderFuncType func(credentialVal string, providerSpec ibmcloudproviderv1.IBMCloudMachineProviderSpec) (Client, error)

// NewClient initilizes a new validated client
func NewClient(credentialVal string, providerSpec ibmcloudproviderv1.IBMCloudMachineProviderSpec) (Client, error) {

	// Authenticator
	authenticator := &core.IamAuthenticator{
		ApiKey: credentialVal,
	}

	// Retrieve IAM Token
	iamToken, err := authenticator.RequestToken()
	if err != nil {
		return nil, err
	}

	// Parse access token retrieved from IAM
	// Ignore "no Keyfunc was provided" error - we only want to extract the account id
	// The token will not be used to perform any further actions
	token, _ := jwt.Parse(iamToken.AccessToken, nil)

	// Extract account ID
	var accountID string
	if claimsObj, ok := token.Claims.(jwt.MapClaims); ok {
		// Check if account key is present
		if accountObj, ok := claimsObj["account"].(map[string]interface{}); ok {
			// Check if bss key is present
			if bss, ok := accountObj["bss"].(string); ok {
				// set accountID
				accountID = bss
			}
		}
	}

	// Check accountID
	if accountID == "" {
		return nil, fmt.Errorf("could not parse account id from token")
	}

	// IC Virtual Private Cloud (VPC) API
	vpcService, err := vpcv1.NewVpcV1(&vpcv1.VpcV1Options{
		Authenticator: authenticator,
	})
	if err != nil {
		return nil, err
	}

	// IC Resource Manager API
	resourceManagerService, err := resourcemanagerv2.NewResourceManagerV2(&resourcemanagerv2.ResourceManagerV2Options{
		Authenticator: authenticator,
	})
	if err != nil {
		return nil, err
	}

	// Get Region and Set Service URL
	regionName := providerSpec.Region
	region, _, err := vpcService.GetRegion(vpcService.NewGetRegionOptions(regionName))
	if err != nil {
		return nil, err
	}

	// Set the Service URL
	err = vpcService.SetServiceURL(fmt.Sprintf("%s/v1", *region.Endpoint))
	if err != nil {
		return nil, err
	}

	return &ibmCloudClient{
		AccountID:              accountID,
		vpcService:             vpcService,
		resourceManagerService: resourceManagerService,
	}, nil
}

// InstanceExistsByName checks if the instance exist in VPC
func (c *ibmCloudClient) InstanceExistsByName(name string, machineProviderConfig *ibmcloudproviderv1.IBMCloudMachineProviderSpec) (bool, error) {
	// Get Instance info
	_, err := c.InstanceGetByName(name, machineProviderConfig)

	// Instance found
	if err == nil {
		return true, nil
	}

	// Instance not found
	if errors.Is(err, errInstanceNotFound) {
		return false, nil
	}

	// Could not retrieve Instances list
	return false, err
}

// InstanceDeleteByName deletes the requested instance
func (c *ibmCloudClient) InstanceDeleteByName(name string, machineProviderConfig *ibmcloudproviderv1.IBMCloudMachineProviderSpec) error {
	// Get Instance info
	getInstance, err := c.InstanceGetByName(name, machineProviderConfig)
	if err != nil {
		return err
	}

	// Get instance ID
	instanceID := *getInstance.ID
	if instanceID == "" {
		return fmt.Errorf("could not get the instance id")
	}

	// Initialize New Delete Instance Options
	deleteInstanceOption := c.vpcService.NewDeleteInstanceOptions(instanceID)
	// // Set Instance ID
	// deleteInstanceOption.SetID(instanceID)

	// Delete the Instance
	_, err = c.vpcService.DeleteInstance(deleteInstanceOption)
	if err != nil {
		return err
	}

	return nil
}

// InstanceGetByName retrieves a single instance specified by Instance Name
func (c *ibmCloudClient) InstanceGetByName(name string, machineProviderConfig *ibmcloudproviderv1.IBMCloudMachineProviderSpec) (*vpcv1.Instance, error) {
	// Region Name
	regionName := machineProviderConfig.Region
	// Get Service URL
	serviceURL := c.vpcService.GetServiceURL()
	// Initialize New List Instances Options
	listInstOptions := c.vpcService.NewListInstancesOptions()
	// Set Image Name
	listInstOptions.SetName(name)
	// Set VPC Name
	vpcName := machineProviderConfig.VPC
	listInstOptions.SetVPCName(vpcName)

	// Get Instances list
	instance, _, err := c.vpcService.ListInstances(listInstOptions)
	if err != nil {
		return nil, err
	}

	// Check if instance is not nil
	if instance == nil {
		return nil, fmt.Errorf("could not retrieve a list of instances - name: %v in region: %v under vpc: %v. service url: %v", name, regionName, vpcName, serviceURL)
	}

	// Found the instance
	if len(instance.Instances) != 0 {
		return &instance.Instances[0], nil
	}

	// Not found
	return nil, errInstanceNotFound
}

// InstanceGetByID retrieves a single instance specified by instanceID
func (c *ibmCloudClient) InstanceGetByID(instanceID string) (*vpcv1.Instance, error) {
	options := c.vpcService.NewGetInstanceOptions(instanceID)

	instance, _, err := c.vpcService.GetInstance(options)
	if err != nil {
		return nil, err
	}

	return instance, nil
}

// InstanceGetProfile returns instance profile info
func (c *ibmCloudClient) InstanceGetProfile(profileName string) (bool, error) {
	// check if profile is set before making an api call
	if profileName == "" {
		return false, fmt.Errorf("instance profile not set")
	}

	// Initialize New List Instance Profiles Options
	listInstanceProfileOptions := c.vpcService.NewGetInstanceProfileOptions(profileName)

	// Get a list of all instance profiles
	_, _, err := c.vpcService.GetInstanceProfile(listInstanceProfileOptions)

	// Instance profile err
	if err != nil {
		return false, err
	}

	// found instance profile
	return true, nil
}

// InstanceCreate creates an instance in VPC
func (c *ibmCloudClient) InstanceCreate(machineName string, machineProviderConfig *ibmcloudproviderv1.IBMCloudMachineProviderSpec, userData string) (*vpcv1.Instance, error) {
	// Get Image ID from Image name
	// Get Subnet ID from Subnet name
	// Get SecurityGroups ID from Security Groups name
	// Get VPC ID from VPC name

	// Get Resource Group ID
	resourceGroupName := machineProviderConfig.ResourceGroup
	resourceGroupID, err := c.GetResourceGroupIDByName(resourceGroupName)
	if err != nil {
		return nil, err
	}

	// Get Custom Image ID
	imageID, err := c.GetCustomImageByName(machineProviderConfig.Image, resourceGroupID)
	if err != nil {
		return nil, err
	}

	// Verify Instance Profile
	instanceProfile, err := c.VerifyInstanceProfile(machineProviderConfig.Profile)
	if err != nil {
		return nil, err
	}

	// Get VPC ID
	vpcName := machineProviderConfig.VPC
	vpcID, err := c.GetVPCIDByName(vpcName, resourceGroupID)
	if err != nil {
		return nil, err
	}

	// Get Subnet ID
	subnetName := machineProviderConfig.PrimaryNetworkInterface.Subnet
	subnetID, err := c.GetSubnetIDbyName(subnetName, resourceGroupID)
	if err != nil {
		return nil, err
	}

	// Get Security Groups
	securityGroups, err := c.GetSecurityGroupsByName(machineProviderConfig.PrimaryNetworkInterface.SecurityGroups, resourceGroupID, vpcID)
	if err != nil {
		return nil, err
	}

	// Get NetworkInterfaces
	networkInterfaces := []vpcv1.NetworkInterfacePrototype{}
	for _, secondaryInterface := range machineProviderConfig.NetworkInterfaces {
		secondarySubnetName := secondaryInterface.Subnet
		secondarySubnetID, err := c.GetSubnetIDbyName(secondarySubnetName, resourceGroupID)
		if err != nil {
			return nil, err
		}
		secondarySecurityGroups, err := c.GetSecurityGroupsByName(secondaryInterface.SecurityGroups, resourceGroupID, vpcID)
		if err != nil {
			return nil, err
		}
		networkInterface := vpcv1.NetworkInterfacePrototype{
			Subnet: &vpcv1.SubnetIdentity{
				ID: &secondarySubnetID,
			},
			SecurityGroups:  secondarySecurityGroups,
			AllowIPSpoofing: &secondaryInterface.AllowIPSpoofing,
		}
		networkInterfaces = append(networkInterfaces, networkInterface)
	}

	// Set Instance Prototype - Contains all the info necessary to provision an instance
	instancePrototypeObj := &vpcv1.InstancePrototype{
		Name: &machineName,
		Image: &vpcv1.ImageIdentity{
			ID: &imageID,
		},
		Profile: &vpcv1.InstanceProfileIdentity{
			Name: &instanceProfile,
		},
		Zone: &vpcv1.ZoneIdentity{
			Name: &machineProviderConfig.Zone,
		},
		ResourceGroup: &vpcv1.ResourceGroupIdentity{
			ID: &resourceGroupID,
		},
		PrimaryNetworkInterface: &vpcv1.NetworkInterfacePrototype{
			Subnet: &vpcv1.SubnetIdentity{
				ID: &subnetID,
			},
			SecurityGroups: securityGroups,
		},
		NetworkInterfaces: networkInterfaces,
		VPC: &vpcv1.VPCIdentity{
			ID: &vpcID,
		},
		UserData: &userData,
	}

	// Get Dedicated Host ID if needed
	if machineProviderConfig.DedicatedHost != "" {
		dedicatedHostID, err := c.GetDedicatedHostByName(machineProviderConfig.DedicatedHost, resourceGroupID, machineProviderConfig.Zone)
		if err != nil {
			return nil, err
		}
		instancePrototypeObj.PlacementTarget = &vpcv1.InstancePlacementTargetPrototypeDedicatedHostIdentity{
			ID: &dedicatedHostID,
		}
	}

	// Create Instance Options
	options := &vpcv1.CreateInstanceOptions{}

	// Ser Instance Prototype
	options.SetInstancePrototype(instancePrototypeObj)

	// Create a new Instance from an instance prototype object
	instance, _, err := c.vpcService.CreateInstance(options)
	if err != nil {
		return nil, err
	}

	return instance, nil
}

// GetVPCIDByName Retrives VPC ID
func (c *ibmCloudClient) GetVPCIDByName(vpcName string, resourceGroupID string) (string, error) {
	// Initialize List Vpcs Options
	vpcOptions := c.vpcService.NewListVpcsOptions()

	// Set Resource Group ID
	vpcOptions.SetResourceGroupID(resourceGroupID)

	// Get a list all VPCs
	vpcList, _, err := c.vpcService.ListVpcs(vpcOptions)
	if err != nil {
		return "", err
	}

	if vpcList != nil {
		for _, eachVPC := range vpcList.Vpcs {
			if *eachVPC.Name == vpcName {
				return *eachVPC.ID, nil
			}
		}
	}

	return "", fmt.Errorf("could not retrieve vpc id of name: %v", vpcName)
}

// GetAccountID retrieves the Account ID for the IBMCloud Client
func (c *ibmCloudClient) GetAccountID() (string, error) {
	if c.AccountID == "" {
		return "", fmt.Errorf("could not retrieve account id")
	}
	return c.AccountID, nil
}

// GetCustomImageByName retrieves custom image from VPC by region and name
func (c *ibmCloudClient) GetCustomImageByName(imageName string, resourceGroupID string) (string, error) {
	// Initialize List Images Options
	listImagesOptions := c.vpcService.NewListImagesOptions()

	// Private images
	listImagesOptions.SetVisibility(vpcv1.ImageVisibilityPrivateConst)
	// Set Resource Group ID
	listImagesOptions.SetResourceGroupID(resourceGroupID)
	// Set Image name
	listImagesOptions.SetName(imageName)

	// List of all the private images in a region
	privateImage, _, err := c.vpcService.ListImages(listImagesOptions)
	if err != nil {
		return "", err
	}

	if privateImage != nil && len(privateImage.Images) != 0 {
		// Return Image ID
		return *privateImage.Images[0].ID, nil
	}

	return "", fmt.Errorf("could not retrieve image id of name: %v", imageName)
}

// VerifyInstanceProfile verifies the provided instance profile exists
func (c *ibmCloudClient) VerifyInstanceProfile(profileName string) (string, error) {
	// Get list of instance profiles
	instanceProfilesList, _, err := c.vpcService.ListInstanceProfiles(c.vpcService.NewListInstanceProfilesOptions())
	if err != nil {
		return "", err
	}

	if instanceProfilesList != nil {
		for _, instanceProfile := range instanceProfilesList.Profiles {
			if *instanceProfile.Name == profileName {
				return profileName, nil
			}
		}
		return "", machine.InvalidMachineConfiguration(fmt.Sprintf("could not find instance profile: %v", profileName))
	}
	return "", fmt.Errorf("no instance profiles found")
}

// GetResourceGroupIDByName retrives a Resource Group ID
func (c *ibmCloudClient) GetResourceGroupIDByName(resourceGroupName string) (string, error) {
	// Initialize New List Resource Group Options
	resourceGroupOptions := c.resourceManagerService.NewListResourceGroupsOptions()
	// Set Resource Group Name
	resourceGroupOptions.SetName(resourceGroupName)
	// Set Account ID
	resourceGroupOptions.SetAccountID(c.AccountID)
	// Get Resource Group
	resourceGroup, _, err := c.resourceManagerService.ListResourceGroups(resourceGroupOptions)
	if err != nil {
		return "", err
	}

	// Check resourceGroup is not nil and Resources[] is not empty
	if resourceGroup != nil && len(resourceGroup.Resources) != 0 {
		// Return Resource Group ID
		return *resourceGroup.Resources[0].ID, nil
	}

	return "", fmt.Errorf("could not retrieve resource group id of name: %v", resourceGroupName)
}

// GetSubnetIDbyName retrives a Subnet ID
func (c *ibmCloudClient) GetSubnetIDbyName(subnetName string, resourceGroupID string) (string, error) {
	// Initialize List Subnets Options
	subnetOption := c.vpcService.NewListSubnetsOptions()

	// Set Resource Group ID
	subnetOption.SetResourceGroupID(resourceGroupID)

	// Get a list of all subnets
	subnetList, _, err := c.vpcService.ListSubnets(subnetOption)
	if err != nil {
		return "", err
	}

	if subnetList != nil {
		for _, eachSubnet := range subnetList.Subnets {
			if *eachSubnet.Name == subnetName {
				// Return Subnet ID
				return *eachSubnet.ID, nil
			}
		}
	}
	return "", fmt.Errorf("could not retrieve subnet id of name: %v", subnetName)
}

// GetSecurityGroupsByName retrieves Security Groups ID
func (c *ibmCloudClient) GetSecurityGroupsByName(securityGroupNames []string, resourceGroupID string, vpcID string) ([]vpcv1.SecurityGroupIdentityIntf, error) {
	// Initialize a map with Security Group Names
	securityGroupMap := map[string]string{}
	for _, item := range securityGroupNames {
		securityGroupMap[item] = ""
	}

	// Initialize List Security Groups Options
	securityGroupOptions := c.vpcService.NewListSecurityGroupsOptions()
	// Set Resource Group ID
	securityGroupOptions.SetResourceGroupID(resourceGroupID)
	// Set VPC ID
	securityGroupOptions.SetVPCID(vpcID)

	// Get a List of Security Groups
	securityGroups, _, _ := c.vpcService.ListSecurityGroups(securityGroupOptions)

	// A slice with 0 len
	var SecurityGroupIdentityList = make([]vpcv1.SecurityGroupIdentityIntf, 0)

	// Make sure securityGroups is not nil
	if securityGroups != nil {
		for _, eachSecurityGroup := range securityGroups.SecurityGroups {
			if _, ok := securityGroupMap[*eachSecurityGroup.Name]; ok {
				SecurityGroupIdentityList = append(SecurityGroupIdentityList, &vpcv1.SecurityGroupIdentityByID{
					ID: eachSecurityGroup.ID,
				})
				// Delete ID from map
				delete(securityGroupMap, *eachSecurityGroup.Name)
			}
		}
	}

	// Check if retrieved all IDs
	if len(securityGroupNames) == len(SecurityGroupIdentityList) {
		return SecurityGroupIdentityList, nil
	}

	return nil, fmt.Errorf("could not retrieve security group ids of names: %v", securityGroupMap)

}

// GetDedicatedHostByName retrieves Dedicated Hosts info
func (c *ibmCloudClient) GetDedicatedHostByName(dedicatedHostName string, resourceGroupID string, zoneName string) (string, error) {
	// Initialize List Dedicated Hosts Options
	dedicatedHostOptions := c.vpcService.NewListDedicatedHostsOptions()

	// Set Resource Group ID
	dedicatedHostOptions.SetResourceGroupID(resourceGroupID)

	// Set Zone
	dedicatedHostOptions.SetZoneName(zoneName)

	// Get a list of all Dedicated Hosts
	dedicatedHosts, _, err := c.vpcService.ListDedicatedHosts(dedicatedHostOptions)
	if err != nil {
		return "", err
	}

	if dedicatedHosts != nil && len(dedicatedHosts.DedicatedHosts) > 0 {
		for _, eachDedicatedHost := range dedicatedHosts.DedicatedHosts {
			if *eachDedicatedHost.Name == dedicatedHostName {
				// return Dedicated Host ID
				return *eachDedicatedHost.ID, nil
			}
		}
	}

	return "", fmt.Errorf("could not retrieve dedicated host id of name: %v", dedicatedHostName)
}
