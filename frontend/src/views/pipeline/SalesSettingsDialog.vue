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
import { useUsersStore } from '@/stores/users'
import { useTeamsStore } from '@/stores/teams'
import { salesSettingsService, type SalesSettings } from '@/services/api'
import { getErrorMessage, unwrapResponse } from '@/lib/api-utils'

const open = defineModel<boolean>('open', { default: false })
const { t } = useI18n()
const usersStore = useUsersStore()
const teamsStore = useTeamsStore()

const settings = ref<SalesSettings>({
  default_country_code: '', currency_symbol: '', lead_sla: { enabled: false, minutes: 15 }, monthly_targets: {}
})
const isSaving = ref(false)
const activeUsers = computed(() => usersStore.users.filter(u => u.is_active))
const autoTeams = computed(() => teamsStore.teams.filter(team => team.assignment_strategy !== 'manual'))

watch(open, async value => {
  if (!value) return
  try {
    const [response] = await Promise.all([
      salesSettingsService.get(),
      usersStore.fetchUsers().catch(() => undefined),
      teamsStore.fetchTeams().catch(() => undefined)
    ])
    const loaded = unwrapResponse<SalesSettings>(response)
    settings.value = {
      ...loaded,
      lead_sla: { ...loaded.lead_sla, minutes: loaded.lead_sla.minutes || 15 },
      monthly_targets: loaded.monthly_targets || {}
    }
  } catch (e) {
    toast.error(getErrorMessage(e, t('sales.failed')))
  }
})

async function save() {
  isSaving.value = true
  try {
    const targets: Record<string, number> = {}
    for (const [id, value] of Object.entries(settings.value.monthly_targets)) {
      const amount = Number(value)
      if (amount > 0) targets[id] = amount
    }
    await salesSettingsService.update({
      ...settings.value,
      lead_sla: { ...settings.value.lead_sla, minutes: Number(settings.value.lead_sla.minutes) },
      monthly_targets: targets
    })
    toast.success(t('sales.saved'))
    open.value = false
  } catch (e) {
    toast.error(getErrorMessage(e, t('sales.failed')))
  } finally {
    isSaving.value = false
  }
}
</script>

<template>
  <Dialog v-model:open="open">
    <DialogContent class="max-w-md max-h-[90vh] overflow-y-auto">
      <DialogHeader>
        <DialogTitle>{{ $t('sales.title') }}</DialogTitle>
        <DialogDescription>{{ $t('sales.description') }}</DialogDescription>
      </DialogHeader>

      <div class="space-y-5">
        <div class="grid grid-cols-2 gap-3">
          <div class="space-y-1.5">
            <Label for="sales-country">{{ $t('sales.countryCode') }}</Label>
            <Input id="sales-country" v-model="settings.default_country_code" inputmode="numeric" maxlength="5" placeholder="91" />
          </div>
          <div class="space-y-1.5">
            <Label for="sales-currency">{{ $t('sales.currency') }}</Label>
            <Input id="sales-currency" v-model="settings.currency_symbol" maxlength="8" placeholder="₹" />
          </div>
          <p class="col-span-2 text-xs text-muted-foreground">{{ $t('sales.countryHint') }}</p>
        </div>

        <section class="space-y-2 rounded-md border p-3">
          <label class="flex items-center gap-2 text-sm font-medium">
            <Switch :checked="settings.lead_sla.enabled" @update:checked="settings.lead_sla.enabled = $event" />
            {{ $t('sales.slaTitle') }}
          </label>
          <p class="text-xs text-muted-foreground">{{ $t('sales.slaHint') }}</p>
          <template v-if="settings.lead_sla.enabled">
            <div class="flex items-center gap-2">
              <Label for="sales-sla-minutes" class="text-xs">{{ $t('sales.slaMinutes') }}</Label>
              <Input id="sales-sla-minutes" v-model.number="settings.lead_sla.minutes" type="number" min="1" max="1440" class="h-9 w-24" />
            </div>
            <Select :model-value="settings.lead_sla.team_id" @update:model-value="(v) => settings.lead_sla.team_id = String(v)">
              <SelectTrigger :aria-label="$t('sales.slaTeam')"><SelectValue :placeholder="$t('sales.slaTeam')" /></SelectTrigger>
              <SelectContent>
                <SelectItem v-for="team in autoTeams" :key="team.id" :value="team.id">{{ team.name }}</SelectItem>
              </SelectContent>
            </Select>
          </template>
        </section>

        <section v-if="activeUsers.length" class="space-y-2">
          <Label>{{ $t('sales.targets') }}</Label>
          <div v-for="user in activeUsers" :key="user.id" class="flex items-center gap-2">
            <span class="min-w-0 flex-1 truncate text-sm">{{ user.full_name || user.email }}</span>
            <Input
              v-model.number="settings.monthly_targets[user.id]"
              type="number"
              min="0"
              class="h-9 w-32"
              :aria-label="$t('sales.targetFor', { name: user.full_name || user.email })"
            />
          </div>
        </section>
      </div>

      <DialogFooter>
        <Button variant="outline" @click="open = false">{{ $t('common.cancel') }}</Button>
        <Button :disabled="isSaving" @click="save">{{ $t('common.save') }}</Button>
      </DialogFooter>
    </DialogContent>
  </Dialog>
</template>
