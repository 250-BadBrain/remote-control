import { createRouter, createWebHashHistory } from 'vue-router'
import Dashboard from '../views/Dashboard.vue'
import Viewer from '../views/Viewer.vue'
import MobilePad from '../views/MobilePad.vue'

const routes = [
  {
    path: '/',
    name: 'Dashboard',
    component: Dashboard,
  },
  {
    path: '/viewer',
    name: 'Viewer',
    component: Viewer,
  },
  {
    path: '/mobile',
    name: 'MobilePad',
    component: MobilePad,
  },
]

const router = createRouter({
  history: createWebHashHistory(),
  routes,
})

export default router
