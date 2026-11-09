import { nextTick } from 'vue'
import { useI18n } from 'vue-i18n'
import { driver, type DriveStep, type Driver } from 'driver.js'
import 'driver.js/dist/driver.css'
import { useAuthStore } from '@/stores/auth'

// Sidebar links to stop at, matched by data-tour. Links the user has no
// permission for are not rendered, so their stops drop out on their own.
const navStops = [
  { id: '/pipeline', title: 'nav.pipeline', description: 'tour.pipeline' },
  { id: '/chat', title: 'nav.chat', description: 'tour.chat' },
  { id: '/tasks', title: 'nav.tasks', description: 'tour.tasks' },
  { id: '/campaigns', title: 'nav.campaigns', description: 'tour.campaigns' },
  { id: '/', title: 'nav.dashboard', description: 'tour.dashboard' },
  { id: '/settings', title: 'nav.settings', description: 'tour.settings' },
  { id: 'user-menu', title: 'tour.replayTitle', description: 'tour.replayDesc' }
]

let activeTour: Driver | null = null

export function useProductTour() {
  const { t } = useI18n()
  const authStore = useAuthStore()

  const seenKey = () => `wapcrm_tour_seen:${authStore.user?.id}`

  function buildSteps(): DriveStep[] {
    // The sidebar is off-canvas on phones, so those steps show as centered cards.
    const sidebarVisible = window.matchMedia('(min-width: 768px)').matches
    const stops: DriveStep[] = navStops
      .filter(stop => document.querySelector(`[data-tour="${stop.id}"]`))
      .map(stop => ({
        element: sidebarVisible ? `[data-tour="${stop.id}"]` : undefined,
        popover: { title: t(stop.title), description: t(stop.description), side: 'right', align: 'start' }
      }))
    return [
      {
        popover: {
          title: t('tour.welcomeTitle'),
          description: t('tour.welcomeDesc'),
          nextBtnText: t('tour.start'),
          showButtons: ['next']
        }
      },
      ...stops
    ]
  }

  async function startTour() {
    if (activeTour?.isActive()) return
    await nextTick()
    const tour = driver({
      steps: buildSteps(),
      popoverClass: 'wapcrm-tour',
      showProgress: true,
      progressText: t('tour.progress', { current: '{{current}}', total: '{{total}}' }),
      nextBtnText: t('tour.next'),
      prevBtnText: t('tour.back'),
      doneBtnText: t('tour.done'),
      showButtons: ['next', 'previous'],
      overlayClickBehavior: 'none',
      onPopoverRender: (popover, { state }) => {
        // driver.js focuses the first button after this hook. Enter should advance, not skip.
        setTimeout(() => popover.nextButton.focus())
        if (state.activeIndex === 0) popover.progress.style.display = 'none'
        if (!tour.hasNextStep()) return
        const skip = document.createElement('button')
        skip.className = 'wapcrm-tour-skip'
        skip.textContent = t('tour.skip')
        skip.addEventListener('click', () => tour.destroy())
        popover.footerButtons.prepend(skip)
      },
      onDestroyed: () => {
        localStorage.setItem(seenKey(), '1')
        activeTour = null
      }
    })
    activeTour = tour
    tour.drive()
  }

  // Shown once per user per browser. Skipped under automation so the overlay
  // does not block e2e runs.
  function startTourIfNew() {
    if (navigator.webdriver || !authStore.user || localStorage.getItem(seenKey())) return
    startTour()
  }

  return { startTour, startTourIfNew }
}
