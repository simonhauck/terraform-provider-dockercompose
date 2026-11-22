package provider

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/mapplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/simonhauck/terraform-provider-dockercompose/internal/composeops"
)

// defaultProjectDirRoot is the parent of each project's directory on the host
// when project_dir is not set.
const defaultProjectDirRoot = "/opt/dockercompose/"

// driftPrefix marks a config_hash that Read replaced because the host drifted.
// It never equals a real hash, so the next plan shows an update with the reason.
const driftPrefix = "drifted: "

var (
	_ resource.Resource                = (*projectResource)(nil)
	_ resource.ResourceWithConfigure   = (*projectResource)(nil)
	_ resource.ResourceWithModifyPlan  = (*projectResource)(nil)
	_ resource.ResourceWithImportState = (*projectResource)(nil)
)

type projectResource struct {
	data *providerData
}

func newProjectResource() resource.Resource {
	return &projectResource{}
}

type projectModel struct {
	ID                     types.String `tfsdk:"id"`
	Name                   types.String `tfsdk:"name"`
	Compose                types.String `tfsdk:"compose"`
	AllResources           types.Bool   `tfsdk:"all_resources"`
	Env                    types.Map    `tfsdk:"env"`
	Profiles               types.List   `tfsdk:"profiles"`
	ProjectDir             types.String `tfsdk:"project_dir"`
	WriteFiles             types.Bool   `tfsdk:"write_files"`
	RemoveOrphans          types.Bool   `tfsdk:"remove_orphans"`
	Wait                   types.Bool   `tfsdk:"wait"`
	WaitTimeout            types.String `tfsdk:"wait_timeout"`
	RemoveVolumesOnDestroy types.Bool   `tfsdk:"remove_volumes_on_destroy"`
	ConfigHash             types.String `tfsdk:"config_hash"`
	Drift                  types.String `tfsdk:"drift"`
	Services               types.Map    `tfsdk:"services"`
}

func (r *projectResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_project"
}

func (r *projectResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "A Docker Compose project, deployed like `docker compose up -d`.\n\n" +
			"Changes are applied in place: compose recreates only the services whose configuration changed. " +
			"On every refresh the provider checks that each service has a running container (or, for one-shot services, " +
			"one that exited successfully), that declared networks and volumes still exist, and that the mirrored files " +
			"are unchanged. Any difference shows up in the next plan.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "The project name.",
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"name": schema.StringAttribute{
				MarkdownDescription: "Compose project name. Changing it replaces the project.",
				Required:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators: []validator.String{
					stringvalidator.RegexMatches(regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`),
						"must contain only lowercase letters, digits, dashes and underscores, and start with a letter or digit"),
				},
			},
			"compose": schema.StringAttribute{
				MarkdownDescription: "Compose file content. Use `file()`, `templatefile()` or `yamlencode({...})`. " +
					"Relative paths resolve against `project_dir` on the Docker host. " +
					"`${VAR}` references are interpolated from `env`, never from the environment of the machine running Terraform.",
				Required: true,
			},
			"all_resources": schema.BoolAttribute{
				MarkdownDescription: "Include resources that no service uses, like `docker compose --all-resources up`. " +
					"Set this to deploy compose files that only define networks or volumes. Defaults to `false`.",
				Optional: true,
				Computed: true,
				Default:  booldefault.StaticBool(false),
			},
			"env": schema.MapAttribute{
				MarkdownDescription: "Interpolation variables, the equivalent of a `.env` file. Stored in state and written to `project_dir/.env` with mode `0600`.",
				ElementType:         types.StringType,
				Optional:            true,
				Sensitive:           true,
			},
			"profiles": schema.ListAttribute{
				MarkdownDescription: "Compose profiles to enable.",
				ElementType:         types.StringType,
				Optional:            true,
			},
			"project_dir": schema.StringAttribute{
				MarkdownDescription: "Absolute project directory on the Docker host. Defaults to `/opt/dockercompose/<name>`. " +
					"The compose file and `.env` are mirrored here when `write_files` is set, so `docker compose` works on the host.",
				Optional: true,
				Computed: true,
				Validators: []validator.String{
					stringvalidator.RegexMatches(regexp.MustCompile(`^/`), "must be an absolute path"),
				},
			},
			"write_files": schema.BoolAttribute{
				MarkdownDescription: "Mirror `compose.yaml` and `.env` into `project_dir`. Defaults to `true`.",
				Optional:            true,
				Computed:            true,
				Default:             booldefault.StaticBool(true),
			},
			"remove_orphans": schema.BoolAttribute{
				MarkdownDescription: "Remove containers of services no longer in the compose file. Defaults to `true`.",
				Optional:            true,
				Computed:            true,
				Default:             booldefault.StaticBool(true),
			},
			"wait": schema.BoolAttribute{
				MarkdownDescription: "Wait until services are running or healthy, like `docker compose up --wait`. Defaults to `true`.",
				Optional:            true,
				Computed:            true,
				Default:             booldefault.StaticBool(true),
			},
			"wait_timeout": schema.StringAttribute{
				MarkdownDescription: "Maximum time to wait, as a Go duration such as `90s` or `5m`. Defaults to `5m`.",
				Optional:            true,
				Computed:            true,
				Default:             stringdefault.StaticString("5m"),
				Validators:          []validator.String{durationValidator{}},
			},
			"remove_volumes_on_destroy": schema.BoolAttribute{
				MarkdownDescription: "Also remove named volumes when the project is destroyed. Defaults to `false`.",
				Optional:            true,
				Computed:            true,
				Default:             booldefault.StaticBool(false),
			},
			"config_hash": schema.StringAttribute{
				MarkdownDescription: "Hash of the inputs that define the deployment. After a refresh that found drift, it starts with `drifted:` and briefly names the differences; `drift` holds the details.",
				Computed:            true,
			},
			"drift": schema.StringAttribute{
				MarkdownDescription: "Details of the differences found during the last refresh, including a diff of a changed `compose.yaml`. " +
					"Empty when the project is in sync. Drift makes the next plan propose an update.",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"services": schema.MapAttribute{
				MarkdownDescription: "State of each service's containers, for example `running` or `exited (0)`.",
				ElementType:         types.StringType,
				Computed:            true,
				PlanModifiers:       []planmodifier.Map{mapplanmodifier.UseStateForUnknown()},
			},
		},
	}
}

func (r *projectResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	data, ok := req.ProviderData.(*providerData)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data", fmt.Sprintf("got %T", req.ProviderData))
		return
	}
	r.data = data
}

func (r *projectResource) engine(ctx context.Context, diags *diag.Diagnostics) *composeops.Engine {
	if r.data == nil {
		diags.AddError("Provider not configured", "The provider configuration is not known yet. Make sure its values are known at apply time.")
		return nil
	}
	engine, err := r.data.Engine(ctx)
	if err != nil {
		diags.AddError("Cannot connect to Docker host", err.Error())
		return nil
	}
	return engine
}

// ModifyPlan fills in project_dir, validates the compose file at plan time,
// and plans config_hash so changes, including drift, show up as updates.
func (r *projectResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		return // destroy
	}
	var plan projectModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if plan.ProjectDir.IsUnknown() {
		var configDir types.String
		resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("project_dir"), &configDir)...)
		if configDir.IsNull() && !plan.Name.IsUnknown() {
			plan.ProjectDir = types.StringValue(defaultProjectDirRoot + plan.Name.ValueString())
		}
	}
	plan.ID = plan.Name

	spec, known, diags := specFromModel(ctx, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	if known {
		if _, err := composeops.Load(ctx, spec); err != nil {
			resp.Diagnostics.AddAttributeError(path.Root("compose"), "Invalid compose project", err.Error())
			return
		}
		plan.ConfigHash = types.StringValue(spec.Hash())
	} else {
		plan.ConfigHash = types.StringUnknown()
	}

	if req.State.Raw.IsNull() {
		plan.Drift = types.StringNull()
	} else {
		var state projectModel
		resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
		if resp.Diagnostics.HasError() {
			return
		}
		// Keep the details from the last refresh in state so Terraform can
		// show them as changes made outside of Terraform. When the plan will
		// apply an update, mark the attribute unknown: apply repairs the
		// drift and clears it.
		plan.Drift = state.Drift
		if plan.Drift.IsUnknown() {
			plan.Drift = types.StringNull()
		}
		if !plan.ConfigHash.Equal(state.ConfigHash) {
			plan.Services = types.MapUnknown(types.StringType)
			if state.Drift.IsNull() || state.Drift.ValueString() == "" {
				plan.Drift = types.StringNull()
			} else {
				plan.Drift = types.StringUnknown()
			}
		}
	}
	resp.Diagnostics.Append(resp.Plan.Set(ctx, &plan)...)
}

func (r *projectResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan projectModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	r.apply(ctx, &plan, &resp.Diagnostics)
	// Set state even after a failed apply: Terraform then taints the resource
	// and replaces it next time, instead of losing track of the containers.
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *projectResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state projectModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	movedDir := state.WriteFiles.ValueBool() && !state.ProjectDir.Equal(plan.ProjectDir)
	stoppedWriting := state.WriteFiles.ValueBool() && !plan.WriteFiles.ValueBool()
	if movedDir || stoppedWriting {
		engine := r.engine(ctx, &resp.Diagnostics)
		if engine == nil {
			return
		}
		old := composeops.Spec{Name: state.Name.ValueString(), ProjectDir: state.ProjectDir.ValueString()}
		if err := engine.RemoveFiles(ctx, old); err != nil {
			resp.Diagnostics.AddError("Cannot remove old project files", err.Error())
			return
		}
	}

	r.apply(ctx, &plan, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// apply brings the project up and records the result in model.
func (r *projectResource) apply(ctx context.Context, model *projectModel, diags *diag.Diagnostics) {
	spec, _, d := specFromModel(ctx, *model)
	diags.Append(d...)
	timeout, err := time.ParseDuration(model.WaitTimeout.ValueString())
	if err != nil {
		diags.AddAttributeError(path.Root("wait_timeout"), "Invalid duration", err.Error())
	}
	engine := r.engine(ctx, diags)
	if diags.HasError() {
		return
	}

	model.ConfigHash = types.StringValue(spec.Hash())
	model.Drift = types.StringNull()
	model.Services = types.MapNull(types.StringType)
	err = engine.Apply(ctx, spec, composeops.ApplyOptions{
		WriteFiles:    model.WriteFiles.ValueBool(),
		RemoveOrphans: model.RemoveOrphans.ValueBool(),
		Wait:          model.Wait.ValueBool(),
		WaitTimeout:   timeout,
	})
	if err != nil {
		diags.AddError("Cannot deploy compose project", err.Error())
	}

	status, statusErr := engine.Status(ctx, spec, composeops.StatusOptions{})
	if statusErr != nil {
		diags.AddWarning("Cannot read project status", statusErr.Error())
		return
	}
	services, d := types.MapValueFrom(ctx, types.StringType, status.Services)
	diags.Append(d...)
	model.Services = services
}

func (r *projectResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state projectModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	engine := r.engine(ctx, &resp.Diagnostics)
	if engine == nil {
		return
	}

	// Import leaves only the name; the rest comes from the files on the host.
	if state.Compose.IsNull() {
		r.readImported(ctx, engine, &state, &resp.Diagnostics)
		if resp.Diagnostics.HasError() {
			return
		}
	}

	spec, _, diags := specFromModel(ctx, state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	status, err := engine.Status(ctx, spec, composeops.StatusOptions{CheckFiles: state.WriteFiles.ValueBool()})
	if err != nil {
		resp.Diagnostics.AddError("Cannot read compose project", err.Error())
		return
	}
	if !status.Exists {
		tflog.Info(ctx, "compose project has no resources, removing from state", map[string]any{"name": spec.Name})
		resp.State.RemoveResource(ctx)
		return
	}

	services, diags := types.MapValueFrom(ctx, types.StringType, status.Services)
	resp.Diagnostics.Append(diags...)
	state.Services = services
	if len(status.Drift) > 0 {
		tflog.Info(ctx, "compose project drifted", map[string]any{"name": spec.Name, "drift": status.Drift})
		state.ConfigHash = types.StringValue(driftPrefix + driftSummary(status.Drift))
		state.Drift = types.StringValue(strings.Join(status.Drift, "\n\n"))
	} else {
		state.ConfigHash = types.StringValue(spec.Hash())
		state.Drift = types.StringNull()
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// driftSummary condenses detailed drift entries to their first line, for the
// compact marker stored in config_hash. The drift attribute keeps the details.
func driftSummary(drift []string) string {
	summaries := make([]string, len(drift))
	for i, entry := range drift {
		summary, _, _ := strings.Cut(entry, "\n")
		summaries[i] = strings.TrimSuffix(summary, ":")
	}
	return strings.Join(summaries, "; ")
}

func (r *projectResource) readImported(ctx context.Context, engine *composeops.Engine, state *projectModel, diags *diag.Diagnostics) {
	spec, err := engine.Discover(ctx, state.Name.ValueString())
	if err != nil {
		diags.AddError("Cannot import compose project",
			fmt.Sprintf("%s\n\nImport reads the compose file and .env that this provider mirrors to the host, so the project must have been deployed with write_files enabled.", err))
		return
	}
	state.Compose = types.StringValue(spec.Compose)
	state.AllResources = types.BoolValue(false)
	state.ProjectDir = types.StringValue(spec.ProjectDir)
	state.Profiles = types.ListNull(types.StringType)
	if len(spec.Env) > 0 {
		env, d := types.MapValueFrom(ctx, types.StringType, spec.Env)
		diags.Append(d...)
		state.Env = env
	} else {
		state.Env = types.MapNull(types.StringType)
	}
	state.WriteFiles = types.BoolValue(true)
	state.RemoveOrphans = types.BoolValue(true)
	state.Wait = types.BoolValue(true)
	state.WaitTimeout = types.StringValue("5m")
	state.RemoveVolumesOnDestroy = types.BoolValue(false)
	state.ConfigHash = types.StringValue(spec.Hash())
	state.Drift = types.StringNull()
}

func (r *projectResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state projectModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	engine := r.engine(ctx, &resp.Diagnostics)
	if engine == nil {
		return
	}
	spec := composeops.Spec{Name: state.Name.ValueString(), ProjectDir: state.ProjectDir.ValueString()}
	err := engine.Destroy(ctx, spec, composeops.DestroyOptions{
		RemoveFiles:   state.WriteFiles.ValueBool(),
		RemoveVolumes: state.RemoveVolumesOnDestroy.ValueBool(),
	})
	if err != nil {
		resp.Diagnostics.AddError("Cannot remove compose project", err.Error())
	}
}

func (r *projectResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("name"), req.ID)...)
}

// specFromModel builds the engine spec. known is false while any input is
// still unknown during planning.
func specFromModel(ctx context.Context, m projectModel) (spec composeops.Spec, known bool, diags diag.Diagnostics) {
	known = !m.Name.IsUnknown() && !m.Compose.IsUnknown() && !m.AllResources.IsUnknown() &&
		!m.ProjectDir.IsUnknown() && !m.Env.IsUnknown() && !m.Profiles.IsUnknown()
	spec = composeops.Spec{
		Name:         m.Name.ValueString(),
		Compose:      m.Compose.ValueString(),
		AllResources: m.AllResources.ValueBool(),
		ProjectDir:   m.ProjectDir.ValueString(),
	}
	if !m.Env.IsNull() && !m.Env.IsUnknown() {
		// Elements can be unknown even when the map is known.
		elems := map[string]types.String{}
		diags.Append(m.Env.ElementsAs(ctx, &elems, false)...)
		spec.Env = make(map[string]string, len(elems))
		for k, v := range elems {
			if v.IsUnknown() {
				known = false
			}
			spec.Env[k] = v.ValueString()
		}
	}
	if !m.Profiles.IsNull() && !m.Profiles.IsUnknown() {
		var profiles []types.String
		diags.Append(m.Profiles.ElementsAs(ctx, &profiles, false)...)
		for _, p := range profiles {
			if p.IsUnknown() {
				known = false
			}
			spec.Profiles = append(spec.Profiles, p.ValueString())
		}
	}
	return spec, known, diags
}

// durationValidator checks that a string parses as a positive Go duration.
type durationValidator struct{}

func (durationValidator) Description(context.Context) string {
	return "must be a positive Go duration such as 90s or 5m"
}

func (v durationValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (v durationValidator) ValidateString(ctx context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	d, err := time.ParseDuration(req.ConfigValue.ValueString())
	if err != nil || d <= 0 {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid duration", v.Description(ctx))
	}
}
