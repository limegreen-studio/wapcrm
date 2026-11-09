<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { toast } from 'vue-sonner'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { leadsService, templatesService } from '@/services/api'
import { getErrorMessage, unwrapResponse } from '@/lib/api-utils'
import { templateVariables } from '@/lib/pipelineBoard'

interface TemplateOption {
  id: string
  name: string
  display_name?: string
  language: string
  category?: string
  header_type: string
  header_content: string
  body_content: string
}

const props = defineProps<{ contactIds: string[] }>()
const open = defineModel<boolean>('open', { default: false })
const emit = defineEmits<{ sent: [] }>()

const { t } = useI18n()
const templates = ref<TemplateOption[]>([])
const templateId = ref('')
const params = ref<Record<string, string>>({})
const isSending = ref(false)

const selected = computed(() => templates.value.find(tpl => tpl.id === templateId.value))
const variables = computed(() => {
  const tpl = selected.value
  if (!tpl) return []
  return templateVariables(tpl.body_content || '', tpl.header_type === 'TEXT' ? tpl.header_content || '' : '')
})
const canSend = computed(() =>
  !!selected.value && variables.value.every(n => (params.value[n] ?? '').trim() !== '')
)

watch(open, async value => {
  if (!value) return
  templateId.value = ''
  params.value = {}
  try {
    const response = await templatesService.list({ status: 'APPROVED', limit: 200 })
    templates.value = (unwrapResponse<{ templates: TemplateOption[] }>(response).templates || [])
      .filter(tpl => !['IMAGE', 'VIDEO', 'DOCUMENT'].includes(tpl.header_type))
  } catch (e) {
    toast.error(getErrorMessage(e, t('bulkTemplate.failed')))
  }
})

async function send() {
  if (!canSend.value) return
  isSending.value = true
  try {
    const templateParams: Record<string, string> = {}
    for (const n of variables.value) templateParams[n] = params.value[n]
    const response = await leadsService.bulkTemplate({
      contact_ids: props.contactIds,
      template_id: templateId.value,
      template_params: templateParams
    })
    const { queued, skipped } = unwrapResponse<{ queued: number; skipped: number }>(response)
    toast.success(t('bulkTemplate.queued', { n: queued, skipped }))
    open.value = false
    emit('sent')
  } catch (e) {
    toast.error(getErrorMessage(e, t('bulkTemplate.failed')))
  } finally {
    isSending.value = false
  }
}
</script>

<template>
  <Dialog v-model:open="open">
    <DialogContent class="max-w-md">
      <DialogHeader>
        <DialogTitle>{{ $t('bulkTemplate.title') }}</DialogTitle>
        <DialogDescription>{{ $t('bulkTemplate.description', { n: contactIds.length }) }}</DialogDescription>
      </DialogHeader>
      <div class="space-y-3">
        <div class="space-y-1.5">
          <Label for="bulk-template">{{ $t('automation.template') }}</Label>
          <Select v-model="templateId">
            <SelectTrigger id="bulk-template"><SelectValue :placeholder="$t('bulkTemplate.choose')" /></SelectTrigger>
            <SelectContent>
              <SelectItem v-for="tpl in templates" :key="tpl.id" :value="tpl.id">{{ tpl.display_name || tpl.name }} ({{ tpl.language }})</SelectItem>
            </SelectContent>
          </Select>
        </div>
        <p v-if="selected" class="rounded-md bg-muted px-3 py-2 text-xs whitespace-pre-line">{{ selected.body_content }}</p>
        <div v-for="name in variables" :key="name" class="flex items-center gap-2">
          <span class="w-20 shrink-0 font-mono text-xs">{{ '{' + '{' + name + '}' + '}' }}</span>
          <Input v-model="params[name]" placeholder="{{name}}" :aria-label="name" />
        </div>
        <p v-if="variables.length" class="text-xs text-muted-foreground">{{ $t('automation.paramsHint') }}</p>
        <p class="text-xs text-muted-foreground">{{ $t('bulkTemplate.cost') }}</p>
      </div>
      <DialogFooter>
        <Button variant="outline" @click="open = false">{{ $t('common.cancel') }}</Button>
        <Button :disabled="!canSend || isSending" @click="send">{{ $t('bulkTemplate.send', { n: contactIds.length }) }}</Button>
      </DialogFooter>
    </DialogContent>
  </Dialog>
</template>
