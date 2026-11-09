<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { toast } from 'vue-sonner'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Switch } from '@/components/ui/switch'
import { Checkbox } from '@/components/ui/checkbox'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { automationService, contactsService, templatesService, type NoReplyRule, type NoReplyStep } from '@/services/api'
import { Plus, Trash2 } from 'lucide-vue-next'
import { debounce } from '@/lib/utils'
import { getErrorMessage, unwrapResponse } from '@/lib/api-utils'

interface TemplateOption {
  id: string
  name: string
  display_name?: string
  language: string
  header_type: string
  header_content: string
  body_content: string
}

const props = defineProps<{ pipelineId?: string; pipelineName?: string }>()

const open = defineModel<boolean>('open', { default: false })

const { t } = useI18n()

const NO_TEMPLATE = '__none__'
const enabled = ref(false)
const hours = ref(24)
const createTask = ref(true)
const taskTitle = ref('')
const templateId = ref<string>(NO_TEMPLATE)
const params = ref<Record<string, string>>({})
const templates = ref<TemplateOption[]>([])
const isSaving = ref(false)
const scope = ref<'org' | 'pipeline'>('org')
const steps = ref<NoReplyStep[]>([])

function addStep() {
  steps.value.push({ hours: 48, create_task: true, task_title: '', template_params: {} })
}

function stepTemplate(step: NoReplyStep) {
  return templates.value.find(tpl => tpl.id === step.template_id)
}

function templateParamNames(tpl: TemplateOption | undefined): string[] {
  if (!tpl) return []
  const names = paramNames(tpl.body_content || '')
  if (tpl.header_type === 'TEXT') {
    for (const n of paramNames(tpl.header_content || '')) if (!names.includes(n)) names.push(n)
  }
  return names
}

function setStepTemplate(step: NoReplyStep, value: unknown) {
  step.template_id = value === NO_TEMPLATE ? undefined : String(value)
  step.template_params = {}
}

function cleanSteps(): NoReplyStep[] {
  return steps.value.map(step => {
    const names = templateParamNames(stepTemplate(step))
    const stepParams: Record<string, string> = {}
    for (const n of names) stepParams[n] = step.template_params?.[n] ?? ''
    return {
      hours: Number(step.hours),
      create_task: step.create_task,
      task_title: step.task_title,
      template_id: step.template_id,
      template_params: step.template_id ? stepParams : undefined
    }
  })
}

const scopedPipelineId = computed(() => scope.value === 'pipeline' ? props.pipelineId : undefined)

// Same placeholder syntax as the server ({{1}}, {{name}})
function paramNames(content: string): string[] {
  const names: string[] = []
  for (const m of content.matchAll(/\{\{\s*([^{}]+?)\s*\}\}/g)) {
    if (!names.includes(m[1])) names.push(m[1])
  }
  return names
}

function paramLabel(name: string): string {
  return '{{' + name + '}}'
}

const selectedTemplate = computed(() => templates.value.find(tpl => tpl.id === templateId.value))

const requiredParams = computed(() => templateParamNames(selectedTemplate.value))

async function loadRule() {
  try {
    const [ruleRes, tplRes] = await Promise.all([
      automationService.getNoReply(scopedPipelineId.value),
      templatesService.list({ status: 'APPROVED', limit: 200 })
    ])
    const rule = unwrapResponse<NoReplyRule>(ruleRes)
    // Media-header templates cannot be sent automatically
    templates.value = (unwrapResponse<{ templates: TemplateOption[] }>(tplRes).templates || [])
      .filter(tpl => !['IMAGE', 'VIDEO', 'DOCUMENT'].includes(tpl.header_type))
    enabled.value = rule.enabled
    hours.value = rule.hours || 24
    createTask.value = rule.enabled ? rule.create_task : true
    taskTitle.value = rule.task_title || ''
    templateId.value = rule.template_id || NO_TEMPLATE
    params.value = { ...(rule.template_params || {}) }
    steps.value = (rule.steps || []).map(step => ({ ...step, template_params: { ...(step.template_params || {}) } }))
  } catch (e) {
    toast.error(getErrorMessage(e, t('automation.failedLoad')))
  }
}

watch(open, value => {
  if (!value) return
  scope.value = 'org'
  loadRule()
})

watch(scope, () => { if (open.value) loadRule() })

// Send the template to one contact now, using the values in the form
interface ContactHit { id: string; name: string; phone_number: string }
const testQuery = ref('')
const testHits = ref<ContactHit[]>([])
const testingId = ref<string | null>(null)

const searchContacts = debounce(async () => {
  const q = testQuery.value.trim()
  if (q.length < 2) {
    testHits.value = []
    return
  }
  try {
    const response = await contactsService.list({ search: q, limit: 5 })
    testHits.value = unwrapResponse<{ contacts: ContactHit[] }>(response).contacts || []
  } catch {
    testHits.value = []
  }
}, 300)

watch(testQuery, () => searchContacts())

async function sendTest(contact: ContactHit) {
  if (templateId.value === NO_TEMPLATE) {
    toast.error(t('automation.testNeedsTemplate'))
    return
  }
  testingId.value = contact.id
  try {
    const templateParams: Record<string, string> = {}
    for (const n of requiredParams.value) templateParams[n] = params.value[n] ?? ''
    await automationService.testNoReply({
      contact_id: contact.id,
      template_id: templateId.value,
      template_params: templateParams
    })
    toast.success(t('automation.testSent', { name: contact.name || contact.phone_number }))
  } catch (e) {
    toast.error(getErrorMessage(e, t('automation.testFailed')))
  } finally {
    testingId.value = null
  }
}

async function save() {
  isSaving.value = true
  try {
    const usingTemplate = templateId.value !== NO_TEMPLATE
    const templateParams: Record<string, string> = {}
    if (usingTemplate) {
      for (const n of requiredParams.value) templateParams[n] = params.value[n] ?? ''
    }
    await automationService.updateNoReply({
      enabled: enabled.value,
      hours: Number(hours.value),
      create_task: createTask.value,
      task_title: taskTitle.value,
      template_id: usingTemplate ? templateId.value : undefined,
      template_params: usingTemplate ? templateParams : undefined,
      steps: cleanSteps()
    }, scopedPipelineId.value)
    toast.success(t('automation.saved'))
    open.value = false
  } catch (e) {
    toast.error(getErrorMessage(e, t('automation.failedSave')))
  } finally {
    isSaving.value = false
  }
}
</script>

<template>
  <Dialog v-model:open="open">
    <DialogContent class="max-w-lg">
      <DialogHeader>
        <DialogTitle>{{ $t('automation.title') }}</DialogTitle>
        <DialogDescription>{{ $t('automation.description') }}</DialogDescription>
      </DialogHeader>

      <div class="space-y-4 max-h-[70vh] overflow-y-auto pr-1">
        <div v-if="pipelineId" class="space-y-1.5">
          <Label for="noreply-scope">{{ $t('automation.scope') }}</Label>
          <Select v-model="scope">
            <SelectTrigger id="noreply-scope"><SelectValue /></SelectTrigger>
            <SelectContent>
              <SelectItem value="org">{{ $t('automation.allPipelines') }}</SelectItem>
              <SelectItem value="pipeline">{{ $t('automation.thisPipeline', { name: pipelineName || '' }) }}</SelectItem>
            </SelectContent>
          </Select>
          <p v-if="scope === 'pipeline'" class="text-xs text-muted-foreground">{{ $t('automation.pipelineHint') }}</p>
        </div>

        <label class="flex items-center gap-2 text-sm font-medium">
          <Switch :checked="enabled" @update:checked="enabled = $event" />
          {{ $t('automation.enable') }}
        </label>

        <div class="space-y-1.5">
          <Label for="noreply-hours">{{ $t('automation.hours') }}</Label>
          <Input id="noreply-hours" v-model.number="hours" type="number" min="1" max="720" class="w-32" />
          <p class="text-xs text-muted-foreground">{{ $t('automation.hoursHint') }}</p>
        </div>

        <div class="space-y-2">
          <label class="flex items-center gap-2 text-sm">
            <Checkbox :checked="createTask" @update:checked="createTask = $event === true" />
            {{ $t('automation.createTask') }}
          </label>
          <Input
            v-if="createTask"
            v-model="taskTitle"
            maxlength="255"
            :placeholder="$t('automation.taskTitlePlaceholder', { n: hours })"
            :aria-label="$t('automation.taskTitle')"
          />
        </div>

        <div class="space-y-1.5">
          <Label for="noreply-template">{{ $t('automation.template') }}</Label>
          <Select v-model="templateId">
            <SelectTrigger id="noreply-template">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem :value="NO_TEMPLATE">{{ $t('automation.noTemplate') }}</SelectItem>
              <SelectItem v-for="tpl in templates" :key="tpl.id" :value="tpl.id">
                {{ tpl.display_name || tpl.name }} ({{ tpl.language }})
              </SelectItem>
            </SelectContent>
          </Select>
          <p v-if="selectedTemplate" class="rounded-md bg-muted px-3 py-2 text-xs whitespace-pre-line">{{ selectedTemplate.body_content }}</p>
          <div v-for="name in requiredParams" :key="name" class="flex items-center gap-2">
            <Label :for="`noreply-param-${name}`" class="w-24 shrink-0 font-mono text-xs">{{ paramLabel(name) }}</Label>
            <Input :id="`noreply-param-${name}`" v-model="params[name]" placeholder="{{name}}" />
          </div>
          <p v-if="requiredParams.length" class="text-xs text-muted-foreground">{{ $t('automation.paramsHint') }}</p>
        </div>

        <div class="space-y-2">
          <div class="flex items-center justify-between">
            <Label>{{ $t('automation.steps') }}</Label>
            <Button type="button" variant="outline" size="sm" class="h-7 text-xs" :disabled="steps.length >= 5" @click="addStep">
              <Plus class="h-3 w-3 mr-1" />{{ $t('automation.addStep') }}
            </Button>
          </div>
          <p class="text-xs text-muted-foreground">{{ $t('automation.stepHint') }}</p>
          <div v-for="(step, i) in steps" :key="i" class="space-y-2 rounded-md border p-3">
            <div class="flex items-center gap-2">
              <Label :for="`step-hours-${i}`" class="text-xs">{{ $t('automation.stepAfter') }}</Label>
              <Input :id="`step-hours-${i}`" v-model.number="step.hours" type="number" min="1" max="720" class="h-8 w-24" />
              <Button type="button" variant="ghost" size="icon" class="ml-auto h-8 w-8" :aria-label="$t('automation.removeStep')" @click="steps.splice(i, 1)">
                <Trash2 class="h-4 w-4" />
              </Button>
            </div>
            <label class="flex items-center gap-2 text-sm">
              <Checkbox :checked="step.create_task" @update:checked="step.create_task = $event === true" />
              {{ $t('automation.createTask') }}
            </label>
            <Input v-if="step.create_task" v-model="step.task_title" maxlength="255" :aria-label="$t('automation.taskTitle')" :placeholder="$t('automation.taskTitle')" />
            <Select :model-value="step.template_id ?? NO_TEMPLATE" @update:model-value="(v) => setStepTemplate(step, v)">
              <SelectTrigger :aria-label="$t('automation.template')"><SelectValue /></SelectTrigger>
              <SelectContent>
                <SelectItem :value="NO_TEMPLATE">{{ $t('automation.noTemplate') }}</SelectItem>
                <SelectItem v-for="tpl in templates" :key="tpl.id" :value="tpl.id">{{ tpl.display_name || tpl.name }} ({{ tpl.language }})</SelectItem>
              </SelectContent>
            </Select>
            <div v-for="name in templateParamNames(stepTemplate(step))" :key="name" class="flex items-center gap-2">
              <span class="w-24 shrink-0 font-mono text-xs">{{ paramLabel(name) }}</span>
              <Input v-model="step.template_params![name]" placeholder="{{name}}" :aria-label="paramLabel(name)" />
            </div>
          </div>
        </div>

        <div v-if="templateId !== NO_TEMPLATE" class="space-y-1.5 rounded-md border p-3">
          <Label for="noreply-test">{{ $t('automation.testTitle') }}</Label>
          <p class="text-xs text-muted-foreground">{{ $t('automation.testHint') }}</p>
          <Input id="noreply-test" v-model="testQuery" :placeholder="$t('automation.testSearch')" />
          <ul v-if="testHits.length" class="divide-y rounded-md border text-sm">
            <li v-for="c in testHits" :key="c.id" class="flex items-center gap-2 px-2 py-1.5">
              <span class="min-w-0 flex-1 truncate">{{ c.name || c.phone_number }} <span class="text-xs text-muted-foreground">{{ c.phone_number }}</span></span>
              <Button size="sm" variant="outline" class="h-7" :disabled="testingId !== null" @click="sendTest(c)">
                {{ testingId === c.id ? $t('automation.testSending') : $t('automation.testSend') }}
              </Button>
            </li>
          </ul>
        </div>
      </div>

      <DialogFooter>
        <Button variant="outline" @click="open = false">{{ $t('common.cancel') }}</Button>
        <Button :disabled="isSaving" @click="save">{{ $t('common.save') }}</Button>
      </DialogFooter>
    </DialogContent>
  </Dialog>
</template>
