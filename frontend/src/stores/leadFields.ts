import { defineStore } from 'pinia'
import { ref } from 'vue'
import { leadsService, type LeadField } from '@/services/api'
import { unwrapResponse } from '@/lib/api-utils'

export const useLeadFieldsStore = defineStore('leadFields', () => {
  const fields = ref<LeadField[]>([])
  const loaded = ref(false)

  async function fetchFields(force = false): Promise<LeadField[]> {
    if (loaded.value && !force) return fields.value
    const response = await leadsService.listFields()
    fields.value = unwrapResponse<{ fields: LeadField[] }>(response).fields || []
    loaded.value = true
    return fields.value
  }

  async function saveFields(next: LeadField[]): Promise<LeadField[]> {
    const response = await leadsService.updateFields(next)
    fields.value = unwrapResponse<{ fields: LeadField[] }>(response).fields || []
    loaded.value = true
    return fields.value
  }

  return { fields, loaded, fetchFields, saveFields }
})
