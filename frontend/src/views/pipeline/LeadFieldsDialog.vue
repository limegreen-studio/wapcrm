<script setup lang="ts">
import { ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { toast } from 'vue-sonner'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { ArrowDown, ArrowUp, Plus, Trash2 } from 'lucide-vue-next'
import { useLeadFieldsStore } from '@/stores/leadFields'
import { getErrorMessage } from '@/lib/api-utils'

interface Row {
  key: string
  label: string
  options: string // comma-separated in the form
  isNew: boolean
}

const open = defineModel<boolean>('open', { default: false })

const { t } = useI18n()
const store = useLeadFieldsStore()
const rows = ref<Row[]>([])
const isSaving = ref(false)

// Key for a new field, derived from its label: "Property Type" -> "property_type"
function slugify(label: string): string {
  const slug = label.toLowerCase().replace(/[^a-z0-9]+/g, '_').replace(/^_+|_+$/g, '').slice(0, 30)
  return /^[a-z]/.test(slug) ? slug : slug ? `f_${slug}`.slice(0, 30) : ''
}

watch(open, async value => {
  if (!value) return
  try {
    await store.fetchFields(true)
  } catch (e) {
    toast.error(getErrorMessage(e, t('lead.failedLoadFields')))
  }
  rows.value = store.fields.map(f => ({
    key: f.key,
    label: f.label,
    options: (f.options || []).join(', '),
    isNew: false
  }))
})

function addRow() {
  rows.value.push({ key: '', label: '', options: '', isNew: true })
}

function onLabelInput(row: Row) {
  if (row.isNew) row.key = slugify(row.label)
}

function move(index: number, delta: number) {
  const target = index + delta
  if (target < 0 || target >= rows.value.length) return
  const [row] = rows.value.splice(index, 1)
  rows.value.splice(target, 0, row)
}

async function save() {
  isSaving.value = true
  try {
    await store.saveFields(rows.value.map(r => ({
      key: r.key.trim(),
      label: r.label.trim(),
      options: r.options.split(',').map(o => o.trim()).filter(Boolean)
    })))
    toast.success(t('lead.fieldsSaved'))
    open.value = false
  } catch (e) {
    toast.error(getErrorMessage(e, t('lead.failedSaveFields')))
  } finally {
    isSaving.value = false
  }
}
</script>

<template>
  <Dialog v-model:open="open">
    <DialogContent class="max-w-2xl">
      <DialogHeader>
        <DialogTitle>{{ $t('lead.editFieldsTitle') }}</DialogTitle>
        <DialogDescription>{{ $t('lead.editFieldsDescription') }}</DialogDescription>
      </DialogHeader>

      <div class="max-h-[55vh] overflow-y-auto space-y-2 pr-1">
        <div class="grid grid-cols-[1fr_9rem_1fr_auto] gap-2 text-xs text-muted-foreground px-1">
          <span>{{ $t('lead.fieldLabel') }}</span>
          <span>{{ $t('lead.fieldKey') }}</span>
          <span>{{ $t('lead.fieldOptions') }}</span>
          <span class="w-28" />
        </div>
        <div v-for="(row, index) in rows" :key="index" class="grid grid-cols-[1fr_9rem_1fr_auto] items-center gap-2">
          <Input v-model="row.label" maxlength="50" :aria-label="$t('lead.fieldLabel')" @input="onLabelInput(row)" />
          <Input
            v-model="row.key"
            maxlength="30"
            class="font-mono text-xs"
            :disabled="!row.isNew"
            :title="row.isNew ? '' : $t('lead.keyLocked')"
            :aria-label="$t('lead.fieldKey')"
          />
          <Input v-model="row.options" :placeholder="$t('lead.optionsPlaceholder')" :aria-label="$t('lead.fieldOptions')" />
          <div class="flex w-28">
            <Button variant="ghost" size="icon" :disabled="index === 0" :aria-label="$t('pipeline.moveUp')" @click="move(index, -1)">
              <ArrowUp class="h-4 w-4" />
            </Button>
            <Button variant="ghost" size="icon" :disabled="index === rows.length - 1" :aria-label="$t('pipeline.moveDown')" @click="move(index, 1)">
              <ArrowDown class="h-4 w-4" />
            </Button>
            <Button variant="ghost" size="icon" :disabled="rows.length === 1" :aria-label="$t('lead.removeField')" @click="rows.splice(index, 1)">
              <Trash2 class="h-4 w-4" />
            </Button>
          </div>
        </div>
      </div>
      <div>
        <Button variant="outline" size="sm" :disabled="rows.length >= 20" @click="addRow">
          <Plus class="h-4 w-4 mr-2" />{{ $t('lead.addField') }}
        </Button>
      </div>
      <p class="text-xs text-muted-foreground">{{ $t('lead.removeNote') }}</p>

      <DialogFooter>
        <Button variant="outline" @click="open = false">{{ $t('common.cancel') }}</Button>
        <Button :disabled="isSaving" @click="save">{{ $t('common.save') }}</Button>
      </DialogFooter>
    </DialogContent>
  </Dialog>
</template>
