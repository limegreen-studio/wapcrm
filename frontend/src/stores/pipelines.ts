import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import {
  pipelinesService,
  type BoardCard,
  type Pipeline,
  type PipelineInput,
  type PipelineStage
} from '@/services/api'
import { unwrapResponse } from '@/lib/api-utils'

export const usePipelinesStore = defineStore('pipelines', () => {
  const pipelines = ref<Pipeline[]>([])
  const loaded = ref(false)
  const loading = ref(false)

  const defaultPipeline = computed<Pipeline | undefined>(
    () => pipelines.value.find(p => p.is_default) ?? pipelines.value[0]
  )

  // stage id -> { stage, pipeline } for quick lookups from a contact's stage_id
  const stageIndex = computed(() => {
    const index = new Map<string, { stage: PipelineStage; pipeline: Pipeline }>()
    for (const pipeline of pipelines.value) {
      for (const stage of pipeline.stages) {
        index.set(stage.id, { stage, pipeline })
      }
    }
    return index
  })

  async function fetchPipelines(force = false): Promise<Pipeline[]> {
    if (loaded.value && !force) return pipelines.value
    loading.value = true
    try {
      const response = await pipelinesService.list()
      const data = unwrapResponse<{ pipelines: Pipeline[] }>(response)
      pipelines.value = data.pipelines || []
      loaded.value = true
      return pipelines.value
    } finally {
      loading.value = false
    }
  }

  async function savePipeline(id: string | null, input: PipelineInput): Promise<Pipeline> {
    const response = id
      ? await pipelinesService.update(id, input)
      : await pipelinesService.create(input)
    const saved = unwrapResponse<Pipeline>(response)
    await fetchPipelines(true)
    return saved
  }

  async function deletePipeline(id: string): Promise<void> {
    await pipelinesService.delete(id)
    await fetchPipelines(true)
  }

  async function moveContact(contactId: string, stageId: string | null, position?: number): Promise<BoardCard> {
    const response = await pipelinesService.moveContact(contactId, stageId, position)
    return unwrapResponse<BoardCard>(response)
  }

  return {
    pipelines,
    loaded,
    loading,
    defaultPipeline,
    stageIndex,
    fetchPipelines,
    savePipeline,
    deletePipeline,
    moveContact
  }
})
