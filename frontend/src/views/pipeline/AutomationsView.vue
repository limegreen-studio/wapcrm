<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { toast } from 'vue-sonner'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Switch } from '@/components/ui/switch'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { PageHeader } from '@/components/shared'
import { Workflow, Plus, Trash2, Pencil, ArrowDown } from 'lucide-vue-next'
import { usePipelinesStore } from '@/stores/pipelines'
import { useUsersStore } from '@/stores/users'
import { useTeamsStore } from '@/stores/teams'
import { automationRulesService, templatesService, type AutomationAction, type AutomationRule } from '@/services/api'
import { getErrorMessage, unwrapResponse } from '@/lib/api-utils'
import { templateVariables } from '@/lib/pipelineBoard'

interface TemplateOption {
  id: string
  name: string
  display_name?: string
  language: string
  header_type: string
  header_content: string
  body_content: string
}

const { t } = useI18n()
const pipelinesStore = usePipelinesStore()
const usersStore = useUsersStore()
const teamsStore = useTeamsStore()

const ANY = '__any__'
const TRIGGERS = ['lead_created', 'first_message', 'stage_changed', 'tag_added']
const ACTION_TYPES = ['assign_team', 'assign_user', 'add_tag', 'move_stage', 'create_task', 'send_template']

const rules = ref<AutomationRule[]>([])
const templates = ref<TemplateOption[]>([])
const isLoading = ref(true)
const editorOpen = ref(false)
const isSaving = ref(false)
const armedDelete = ref<string | null>(null)

function blankRule(): AutomationRule {
  return { name: '', enabled: true, trigger: 'stage_changed', tag: '', source: '', actions: [blankAction()] }
}

function blankAction(): AutomationAction {
  return { type: 'create_task', delay_hours: 0, task_title: '', task_due_hours: 0 }
}

const draft = ref<AutomationRule>(blankRule())

const allStages = computed(() =>
  pipelinesStore.pipelines.flatMap(p => p.stages.map(s => ({
    id: s.id,
    label: pipelinesStore.pipelines.length > 1 ? `${p.name} · ${s.name}` : s.name
  })))
)
const activeUsers = computed(() => usersStore.users.filter(u => u.is_active))
const showStageFilter = computed(() => draft.value.trigger === 'stage_changed')

async function load() {
  isLoading.value = true
  try {
    const [ruleRes, tplRes] = await Promise.all([
      automationRulesService.list(),
      templatesService.list({ status: 'APPROVED', limit: 200 })
    ])
    rules.value = unwrapResponse<{ rules: AutomationRule[] }>(ruleRes).rules || []
    templates.value = (unwrapResponse<{ templates: TemplateOption[] }>(tplRes).templates || [])
      .filter(tpl => !['IMAGE', 'VIDEO', 'DOCUMENT'].includes(tpl.header_type))
  } catch (e) {
    toast.error(getErrorMessage(e, t('rules.failedLoad')))
  } finally {
    isLoading.value = false
  }
}

onMounted(() => {
  load()
  pipelinesStore.fetchPipelines().catch(() => { /* stage pickers stay empty */ })
  usersStore.fetchUsers().catch(() => { /* user picker stays empty */ })
  teamsStore.fetchTeams().catch(() => { /* team picker stays empty */ })
})

function openEditor(rule?: AutomationRule) {
  draft.value = rule ? JSON.parse(JSON.stringify(rule)) : blankRule()
  editorOpen.value = true
}

function actionTemplate(action: AutomationAction) {
  return templates.value.find(tpl => tpl.id === action.template_id)
}

function actionVariables(action: AutomationAction): string[] {
  const tpl = actionTemplate(action)
  if (!tpl) return []
  return templateVariables(tpl.body_content || '', tpl.header_type === 'TEXT' ? tpl.header_content || '' : '')
}

function setActionType(action: AutomationAction, type: unknown) {
  Object.assign(action, {
    type: String(type), user_id: undefined, team_id: undefined, tag: undefined, stage_id: undefined,
    task_title: undefined, task_due_hours: undefined, template_id: undefined, template_params: undefined
  })
}

function setTemplate(action: AutomationAction, value: unknown) {
  action.template_id = String(value)
  action.template_params = {}
}

async function save() {
  isSaving.value = true
  try {
    const body = JSON.parse(JSON.stringify(draft.value)) as AutomationRule
    if (body.trigger !== 'stage_changed') body.stage_id = undefined
    for (const action of body.actions) action.delay_hours = Number(action.delay_hours) || 0
    if (draft.value.id) await automationRulesService.update(draft.value.id, body)
    else await automationRulesService.create(body)
    toast.success(t('rules.saved'))
    editorOpen.value = false
    await load()
  } catch (e) {
    toast.error(getErrorMessage(e, t('rules.failedSave')))
  } finally {
    isSaving.value = false
  }
}

async function toggle(rule: AutomationRule, enabled: boolean) {
  try {
    await automationRulesService.update(rule.id!, { ...rule, enabled })
    rule.enabled = enabled
  } catch (e) {
    toast.error(getErrorMessage(e, t('rules.failedSave')))
  }
}

async function remove(rule: AutomationRule) {
  if (armedDelete.value !== rule.id) {
    armedDelete.value = rule.id!
    return
  }
  try {
    await automationRulesService.delete(rule.id!)
    rules.value = rules.value.filter(r => r.id !== rule.id)
    armedDelete.value = null
  } catch (e) {
    toast.error(getErrorMessage(e, t('rules.failedSave')))
  }
}

function stageName(id?: string) {
  return allStages.value.find(s => s.id === id)?.label ?? ''
}

function whenLabel(rule: AutomationRule): string {
  const parts = [t(`rules.trigger.${rule.trigger}`)]
  if (rule.stage_id) parts.push(stageName(rule.stage_id))
  if (rule.tag) parts.push(`#${rule.tag}`)
  if (rule.source) parts.push(rule.source)
  return parts.join(' · ')
}

function actionLabel(action: AutomationAction): string {
  const base = t(`rules.action.${action.type}`)
  return action.delay_hours ? `${base} (${t('rules.afterHours', { n: action.delay_hours })})` : base
}
</script>

<template>
  <div class="flex flex-col h-full bg-background light:bg-gray-50">
    <PageHeader
      :title="$t('rules.title')"
      :description="$t('rules.subtitle')"
      :icon="Workflow"
      icon-gradient="bg-gradient-to-br from-pink-500 to-rose-600 shadow-pink-500/20"
    >
      <template #actions>
        <Button size="sm" @click="openEditor()">
          <Plus class="h-4 w-4 mr-2" />{{ $t('rules.new') }}
        </Button>
      </template>
    </PageHeader>

    <div class="flex-1 overflow-y-auto p-4 sm:p-6">
      <p v-if="!isLoading && !rules.length" class="py-16 text-center text-sm text-white/50 light:text-gray-500">{{ $t('rules.empty') }}</p>
      <ul class="space-y-3 max-w-3xl">
        <li
          v-for="rule in rules"
          :key="rule.id"
          class="rounded-xl border border-white/[0.08] light:border-gray-200 bg-white/[0.02] light:bg-white p-4"
        >
          <div class="flex items-start gap-3">
            <Switch class="mt-0.5" :checked="rule.enabled" :aria-label="$t('rules.enabled')" @update:checked="(v: boolean) => toggle(rule, v)" />
            <div class="min-w-0 flex-1">
              <p class="text-sm font-medium text-white light:text-gray-900 truncate">{{ rule.name }}</p>
              <p class="mt-0.5 text-xs text-white/50 light:text-gray-500">{{ whenLabel(rule) }}</p>
              <ul class="mt-2 flex flex-wrap gap-1.5">
                <li
                  v-for="(action, i) in rule.actions"
                  :key="i"
                  class="rounded-full bg-white/[0.06] light:bg-gray-100 px-2 py-0.5 text-xs text-white/70 light:text-gray-700"
                >{{ actionLabel(action) }}</li>
              </ul>
            </div>
            <Button variant="ghost" size="icon" class="h-9 w-9 shrink-0" :aria-label="$t('rules.edit')" @click="openEditor(rule)">
              <Pencil class="h-4 w-4" />
            </Button>
            <Button
              :variant="armedDelete === rule.id ? 'destructive' : 'ghost'"
              size="icon"
              class="h-9 w-9 shrink-0"
              :aria-label="armedDelete === rule.id ? $t('rules.confirmDelete') : $t('rules.delete')"
              :title="armedDelete === rule.id ? $t('rules.confirmDelete') : $t('rules.delete')"
              @click="remove(rule)"
            >
              <Trash2 class="h-4 w-4" />
            </Button>
          </div>
        </li>
      </ul>
    </div>

    <Dialog v-model:open="editorOpen">
      <DialogContent class="max-w-lg max-h-[90vh] overflow-y-auto">
        <DialogHeader>
          <DialogTitle>{{ draft.id ? $t('rules.edit') : $t('rules.new') }}</DialogTitle>
        </DialogHeader>

        <form class="space-y-4" @submit.prevent="save">
          <div class="space-y-1.5">
            <Label for="rule-name">{{ $t('rules.name') }}</Label>
            <Input id="rule-name" v-model="draft.name" maxlength="100" />
          </div>

          <div class="space-y-1.5">
            <Label for="rule-trigger">{{ $t('rules.when') }}</Label>
            <Select v-model="draft.trigger">
              <SelectTrigger id="rule-trigger"><SelectValue /></SelectTrigger>
              <SelectContent>
                <SelectItem v-for="x in TRIGGERS" :key="x" :value="x">{{ $t(`rules.trigger.${x}`) }}</SelectItem>
              </SelectContent>
            </Select>
          </div>

          <div class="grid grid-cols-1 sm:grid-cols-2 gap-3">
            <div v-if="showStageFilter" class="space-y-1.5 sm:col-span-2">
              <Label for="rule-stage">{{ $t('rules.onStage') }}</Label>
              <Select :model-value="draft.stage_id ?? ANY" @update:model-value="(v) => draft.stage_id = v === ANY ? undefined : String(v)">
                <SelectTrigger id="rule-stage"><SelectValue /></SelectTrigger>
                <SelectContent>
                  <SelectItem :value="ANY">{{ $t('rules.anyStage') }}</SelectItem>
                  <SelectItem v-for="s in allStages" :key="s.id" :value="s.id">{{ s.label }}</SelectItem>
                </SelectContent>
              </Select>
            </div>
            <div class="space-y-1.5">
              <Label for="rule-tag">{{ draft.trigger === 'tag_added' ? $t('rules.tagAdded') : $t('rules.hasTag') }}</Label>
              <Input id="rule-tag" v-model="draft.tag" maxlength="50" :placeholder="$t('rules.optional')" />
            </div>
            <div class="space-y-1.5">
              <Label for="rule-source">{{ $t('rules.source') }}</Label>
              <Input id="rule-source" v-model="draft.source" maxlength="100" :placeholder="$t('rules.optional')" />
            </div>
          </div>

          <div class="space-y-2">
            <Label>{{ $t('rules.then') }}</Label>
            <template v-for="(action, i) in draft.actions" :key="i">
              <ArrowDown v-if="i > 0" class="mx-auto h-3.5 w-3.5 text-muted-foreground" />
              <div class="space-y-2 rounded-md border p-3">
                <div class="flex items-center gap-2">
                  <Select :model-value="action.type" @update:model-value="(v) => setActionType(action, v)">
                    <SelectTrigger class="flex-1" :aria-label="$t('rules.actionType')"><SelectValue /></SelectTrigger>
                    <SelectContent>
                      <SelectItem v-for="x in ACTION_TYPES" :key="x" :value="x">{{ $t(`rules.action.${x}`) }}</SelectItem>
                    </SelectContent>
                  </Select>
                  <Button
                    type="button"
                    variant="ghost"
                    size="icon"
                    class="h-9 w-9 shrink-0"
                    :disabled="draft.actions.length === 1"
                    :aria-label="$t('rules.removeAction')"
                    @click="draft.actions.splice(i, 1)"
                  >
                    <Trash2 class="h-4 w-4" />
                  </Button>
                </div>

                <Select v-if="action.type === 'assign_team'" :model-value="action.team_id" @update:model-value="(v) => action.team_id = String(v)">
                  <SelectTrigger :aria-label="$t('rules.action.assign_team')"><SelectValue :placeholder="$t('rules.chooseTeam')" /></SelectTrigger>
                  <SelectContent>
                    <SelectItem v-for="team in teamsStore.teams" :key="team.id" :value="team.id">{{ team.name }}</SelectItem>
                  </SelectContent>
                </Select>

                <Select v-if="action.type === 'assign_user'" :model-value="action.user_id" @update:model-value="(v) => action.user_id = String(v)">
                  <SelectTrigger :aria-label="$t('rules.action.assign_user')"><SelectValue :placeholder="$t('rules.chooseUser')" /></SelectTrigger>
                  <SelectContent>
                    <SelectItem v-for="u in activeUsers" :key="u.id" :value="u.id">{{ u.full_name || u.email }}</SelectItem>
                  </SelectContent>
                </Select>

                <Input v-if="action.type === 'add_tag'" v-model="action.tag" maxlength="50" :aria-label="$t('pipeline.filterTag')" :placeholder="$t('pipeline.filterTag')" />

                <Select v-if="action.type === 'move_stage'" :model-value="action.stage_id" @update:model-value="(v) => action.stage_id = String(v)">
                  <SelectTrigger :aria-label="$t('rules.action.move_stage')"><SelectValue :placeholder="$t('rules.chooseStage')" /></SelectTrigger>
                  <SelectContent>
                    <SelectItem v-for="s in allStages" :key="s.id" :value="s.id">{{ s.label }}</SelectItem>
                  </SelectContent>
                </Select>

                <template v-if="action.type === 'create_task'">
                  <Input v-model="action.task_title" maxlength="255" :aria-label="$t('tasks.title')" :placeholder="$t('tasks.title')" />
                  <div class="flex items-center gap-2">
                    <Label :for="`rule-due-${i}`" class="text-xs">{{ $t('rules.dueIn') }}</Label>
                    <Input :id="`rule-due-${i}`" v-model.number="action.task_due_hours" type="number" min="0" max="720" class="h-9 w-24" />
                  </div>
                </template>

                <template v-if="action.type === 'send_template'">
                  <Select :model-value="action.template_id" @update:model-value="(v) => setTemplate(action, v)">
                    <SelectTrigger :aria-label="$t('automation.template')"><SelectValue :placeholder="$t('bulkTemplate.choose')" /></SelectTrigger>
                    <SelectContent>
                      <SelectItem v-for="tpl in templates" :key="tpl.id" :value="tpl.id">{{ tpl.display_name || tpl.name }} ({{ tpl.language }})</SelectItem>
                    </SelectContent>
                  </Select>
                  <div v-for="name in actionVariables(action)" :key="name" class="flex items-center gap-2">
                    <span class="w-20 shrink-0 font-mono text-xs">{{ '{' + '{' + name + '}' + '}' }}</span>
                    <Input v-model="action.template_params![name]" placeholder="{{name}}" :aria-label="name" />
                  </div>
                  <p v-if="draft.trigger === 'lead_created'" class="text-xs text-amber-500">{{ $t('rules.publicFormWarning') }}</p>
                </template>

                <div class="flex items-center gap-2">
                  <Label :for="`rule-delay-${i}`" class="text-xs">{{ $t('rules.waitHours') }}</Label>
                  <Input :id="`rule-delay-${i}`" v-model.number="action.delay_hours" type="number" min="0" max="720" class="h-9 w-24" />
                </div>
              </div>
            </template>
            <Button type="button" variant="outline" size="sm" :disabled="draft.actions.length >= 10" @click="draft.actions.push(blankAction())">
              <Plus class="h-3.5 w-3.5 mr-1.5" />{{ $t('rules.addAction') }}
            </Button>
            <p class="text-xs text-muted-foreground">{{ $t('rules.dripHint') }}</p>
          </div>

          <label class="flex items-center gap-2 text-sm font-medium">
            <Switch :checked="draft.enabled" @update:checked="draft.enabled = $event" />
            {{ $t('rules.enabled') }}
          </label>

          <DialogFooter>
            <Button type="button" variant="outline" @click="editorOpen = false">{{ $t('common.cancel') }}</Button>
            <Button type="submit" :disabled="isSaving || !draft.name.trim()">{{ $t('common.save') }}</Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  </div>
</template>
