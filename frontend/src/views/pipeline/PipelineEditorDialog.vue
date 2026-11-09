<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { toast } from 'vue-sonner'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Switch } from '@/components/ui/switch'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { ArrowDown, ArrowUp, Plus, Trash2 } from 'lucide-vue-next'
import { usePipelinesStore } from '@/stores/pipelines'
import type { PipelineStageInput, StageKind } from '@/services/api'
import { TAG_COLORS, getStageDotClass } from '@/lib/constants'
import { getErrorMessage } from '@/lib/api-utils'

const props = defineProps<{
  pipelineId: string | null
}>()

const open = defineModel<boolean>('open', { default: false })

const emit = defineEmits<{
  saved: [pipelineId: string | null]
}>()

const { t } = useI18n()
const pipelinesStore = usePipelinesStore()

// null = creating a new pipeline
const editingId = ref<string | null>(null)
const name = ref('')
const isDefault = ref(false)
const stages = ref<PipelineStageInput[]>([])
const isSaving = ref(false)
const confirmDelete = ref(false)

const KINDS: StageKind[] = ['open', 'won', 'lost']

function load(id: string | null) {
  editingId.value = id
  confirmDelete.value = false
  const pipeline = id ? pipelinesStore.pipelines.find(p => p.id === id) : undefined
  if (pipeline) {
    name.value = pipeline.name
    isDefault.value = pipeline.is_default
    stages.value = pipeline.stages.map(s => ({ id: s.id, name: s.name, color: s.color || 'gray', kind: s.kind, probability: s.probability ?? 0 }))
  } else {
    name.value = ''
    isDefault.value = pipelinesStore.pipelines.length === 0
    stages.value = [
      { name: t('pipeline.defaultStageNew'), color: 'blue', kind: 'open', probability: 10 },
      { name: t('pipeline.defaultStageWon'), color: 'green', kind: 'won', probability: 100 },
      { name: t('pipeline.defaultStageLost'), color: 'red', kind: 'lost', probability: 0 }
    ]
  }
}

watch(open, value => {
  if (value) load(props.pipelineId)
})

const title = computed(() => (editingId.value ? t('pipeline.editTitle') : t('pipeline.newTitle')))

function addStage() {
  stages.value.push({ name: '', color: 'gray', kind: 'open', probability: 50 })
}

function removeStage(index: number) {
  stages.value.splice(index, 1)
}

function moveStage(index: number, delta: number) {
  const target = index + delta
  if (target < 0 || target >= stages.value.length) return
  const [stage] = stages.value.splice(index, 1)
  stages.value.splice(target, 0, stage)
}

async function save() {
  if (!name.value.trim()) {
    toast.error(t('pipeline.nameRequired'))
    return
  }
  isSaving.value = true
  try {
    const saved = await pipelinesStore.savePipeline(editingId.value, {
      name: name.value,
      is_default: isDefault.value,
      stages: stages.value
    })
    toast.success(t('pipeline.saved'))
    open.value = false
    emit('saved', saved.id)
  } catch (e) {
    toast.error(getErrorMessage(e, t('pipeline.failedSave')))
  } finally {
    isSaving.value = false
  }
}

async function remove() {
  if (!editingId.value) return
  if (!confirmDelete.value) {
    confirmDelete.value = true
    return
  }
  isSaving.value = true
  try {
    await pipelinesStore.deletePipeline(editingId.value)
    toast.success(t('pipeline.deleted'))
    open.value = false
    emit('saved', null)
  } catch (e) {
    toast.error(getErrorMessage(e, t('pipeline.failedSave')))
  } finally {
    isSaving.value = false
  }
}
</script>

<template>
  <Dialog v-model:open="open">
    <DialogContent class="max-w-2xl">
      <DialogHeader>
        <DialogTitle>{{ title }}</DialogTitle>
        <DialogDescription>{{ $t('pipeline.editorDescription') }}</DialogDescription>
      </DialogHeader>

      <div class="space-y-4">
        <div class="flex items-end gap-4">
          <div class="flex-1 space-y-1.5">
            <Label for="pipeline-name">{{ $t('pipeline.name') }}</Label>
            <Input id="pipeline-name" v-model="name" maxlength="100" />
          </div>
          <label class="flex items-center gap-2 pb-2 text-sm">
            <Switch :checked="isDefault" @update:checked="isDefault = $event" />
            {{ $t('pipeline.isDefault') }}
          </label>
        </div>

        <div class="space-y-2">
          <Label>{{ $t('pipeline.stages') }}</Label>
          <div class="max-h-[50vh] overflow-y-auto space-y-2 pr-1">
            <div v-for="(stage, index) in stages" :key="stage.id ?? `new-${index}`" class="flex flex-wrap items-center gap-2 rounded-md border p-2 sm:flex-nowrap sm:border-0 sm:p-0">
              <Input
                v-model="stage.name"
                class="flex-1"
                maxlength="100"
                :placeholder="$t('pipeline.stageName')"
                :aria-label="$t('pipeline.stageName')"
              />
              <Select v-model="stage.color">
                <SelectTrigger class="w-28" :aria-label="$t('pipeline.stageColor')">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem v-for="color in TAG_COLORS" :key="color.value" :value="color.value" :text-value="color.label">
                    <div class="flex items-center gap-2">
                      <span :class="['w-3 h-3 rounded-full', getStageDotClass(color.value)]" />
                      {{ color.label }}
                    </div>
                  </SelectItem>
                </SelectContent>
              </Select>
              <Input
                v-model.number="stage.probability"
                type="number"
                min="0"
                max="100"
                class="w-20"
                :aria-label="$t('pipeline.stageProbability')"
                :title="$t('pipeline.stageProbability')"
              />
              <Select v-model="stage.kind">
                <SelectTrigger class="w-28" :aria-label="$t('pipeline.stageKind')">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem v-for="kind in KINDS" :key="kind" :value="kind">{{ $t(`pipeline.kind.${kind}`) }}</SelectItem>
                </SelectContent>
              </Select>
              <Button variant="ghost" size="icon" :disabled="index === 0" :aria-label="$t('pipeline.moveUp')" @click="moveStage(index, -1)">
                <ArrowUp class="h-4 w-4" />
              </Button>
              <Button variant="ghost" size="icon" :disabled="index === stages.length - 1" :aria-label="$t('pipeline.moveDown')" @click="moveStage(index, 1)">
                <ArrowDown class="h-4 w-4" />
              </Button>
              <Button variant="ghost" size="icon" :disabled="stages.length === 1" :aria-label="$t('pipeline.removeStage')" @click="removeStage(index)">
                <Trash2 class="h-4 w-4" />
              </Button>
            </div>
          </div>
          <Button variant="outline" size="sm" @click="addStage">
            <Plus class="h-4 w-4 mr-2" />{{ $t('pipeline.addStage') }}
          </Button>
        </div>
      </div>

      <DialogFooter class="flex-wrap gap-2 sm:justify-between">
        <div class="flex gap-2">
          <Button v-if="editingId" variant="destructive" size="sm" :disabled="isSaving" @click="remove">
            {{ confirmDelete ? $t('pipeline.confirmDelete') : $t('pipeline.deletePipeline') }}
          </Button>
          <Button v-if="editingId" variant="ghost" size="sm" @click="load(null)">
            {{ $t('pipeline.newPipeline') }}
          </Button>
        </div>
        <div class="flex gap-2">
          <Button variant="outline" @click="open = false">{{ $t('common.cancel') }}</Button>
          <Button :disabled="isSaving" @click="save">{{ $t('common.save') }}</Button>
        </div>
      </DialogFooter>
    </DialogContent>
  </Dialog>
</template>
