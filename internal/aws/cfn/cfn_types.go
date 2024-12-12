package cfn

import "github.com/aws-cloudformation/rain/cft"

type StackSetConfig struct {
	StackSetName                  string            `yaml:"StackSetName"`
	Template                      cft.Template      `yaml:"-"`
	Parameters                    map[string]string `yaml:"Parameters"`
	Tags                          map[string]string `yaml:"Tags"`
	AdministrationRoleARN         string            `yaml:"AdministrationRoleARN"`
	AutoDeploymentEnabled         bool              `yaml:"AutoDeploymentEnabled"`
	CallAsSelf                    bool              `yaml:"CallAsSelf"`
	RetainStacksOnAccountRemoval  bool              `yaml:"RetainStacksOnAccountRemoval"`
	Description                   string            `yaml:"Description"`
	ExecutionRoleName             string            `yaml:"ExecutionRoleName"`
	ManagedExecutionActive        bool              `yaml:"ManagedExecutionActive"`
	PermissionModelServiceManaged bool              `yaml:"PermissionModelServiceManaged"`
	Regions                       []string          `yaml:"Regions"`
	Accounts                      []string          `yaml:"Accounts"`
	AccountFilterType             string            `yaml:"AccountFilterType"`
	OrganizationalUnitIds         []string          `yaml:"OrganizationalUnitIds"`
	ConcurrencyMode               string            `yaml:"ConcurrencyMode"`
	FailureTolerance              int               `yaml:"FailureTolerance"`
	FailureTolerancePercentage    int               `yaml:"FailureTolerancePercentage"`
	MaxConcurrentCount            int               `yaml:"MaxConcurrentCount"`
	MaxConcurrentPercentage       *int32            `yaml:"MaxConcurrentPercentage"`
	RegionConcurrencySequential   bool              `yaml:"RegionConcurrencySequential"`
	RegionOrder                   []string          `yaml:"RegionOrder"`
}