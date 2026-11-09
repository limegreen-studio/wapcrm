<script setup lang="ts">
import { onMounted, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { toast } from 'vue-sonner'
import { Input } from '@/components/ui/input'
import { Loader2, ClipboardList } from 'lucide-vue-next'
import { leadsService } from '@/services/api'
import { useLeadFieldsStore } from '@/stores/leadFields'
import { getErrorMessage, unwrapResponse } from '@/lib/api-utils'

const props = defineProps<{
  contactId: string
  metadata: Record<string, any> | null | undefined
  canEdit: boolean
}>()

const emit = defineEmits<{
  updated: [metadata: Record<string, any>]
}>()

const { t } = useI18n()
const leadFieldsStore = useLeadFieldsStore()

// Draft values per field; saved values are what the server last returned
const draft = reactive<Record<string, string>>({})
const saved = ref<Record<string, string>>({})
const savingKey = ref<string | null>(null)

onMounted(() => {
  leadFieldsStore.fetchFields().catch(() => { /* section stays empty */ })
})

function readLead(metadata: Record<string, any> | null | undefined): Record<string, string> {
  const lead = metadata?.lead
  const out: Record<string, string> = {}
  if (lead && typeof lead === 'object') {
    for (const [key, v] of Object.entries(lead)) {
      out[key] = v === undefined || v === null ? '' : String(v)
    }
  }
  return out
}

watch(
  () => [props.contactId, props.metadata, leadFieldsStore.fields] as const,
  ([, metadata]) => {
    const values = readLead(metadata)
    saved.value = values
    // Keep an in-progress edit on the field being saved
    for (const f of leadFieldsStore.fields) {
      if (f.key !== savingKey.value) draft[f.key] = values[f.key] ?? ''
    }
  },
  { immediate: true, deep: true }
)

async function save(key: string) {
  const value = (draft[key] ?? '').trim()
  if (value === (saved.value[key] ?? '') || savingKey.value) return
  savingKey.value = key
  try {
    const response = await leadsService.update(props.contactId, { [key]: value })
    const data = unwrapResponse<{ metadata: Record<string, any> }>(response)
    saved.value = { ...saved.value, [key]: value }
    emit('updated', data.metadata)
  } catch (e) {
    draft[key] = saved.value[key] ?? ''
    toast.error(getErrorMessage(e, t('lead.failedSave')))
  } finally {
    savingKey.value = null
  }
}
</script>

<template>
  <div v-if="leadFieldsStore.fields.length" class="pb-4 border-b">
    <h5 class="text-sm font-medium flex items-center gap-2 py-2">
      <ClipboardList class="h-4 w-4 text-muted-foreground" />
      {{ t('lead.title') }}
      <Loader2 v-if="savingKey" class="h-3.5 w-3.5 animate-spin text-muted-foreground" />
    </h5>
    <dl class="grid grid-cols-[6rem_1fr] items-center gap-x-2 gap-y-1.5 text-sm">
      <template v-for="field in leadFieldsStore.fields" :key="field.key">
        <dt class="text-muted-foreground truncate" :title="field.label">
          <label :for="`lead-${field.key}`">{{ field.label }}</label>
        </dt>
        <dd class="min-w-0">
          <Input
            v-if="canEdit"
            :id="`lead-${field.key}`"
            v-model="draft[field.key]"
            class="h-7 text-sm"
            maxlength="255"
            :list="field.options?.length ? `lead-${field.key}-options` : undefined"
            :placeholder="t('lead.empty')"
            @blur="save(field.key)"
            @keydown.enter.prevent="($event.target as HTMLInputElement).blur()"
          />
          <span v-else class="block truncate">{{ saved[field.key] || '—' }}</span>
          <datalist v-if="field.options?.length" :id="`lead-${field.key}-options`">
            <option v-for="opt in field.options" :key="opt" :value="opt" />
          </datalist>
        </dd>
      </template>
    </dl>
  </div>
</template>
