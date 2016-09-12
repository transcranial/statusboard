Vue.http.get('config.json').then(res => {
  const config = typeof res.body === 'string' ? JSON.parse(res.body) : res.body
  const checks = config.checks

  let checksData = {}
  checks.forEach(function (check) {
    checksData[check.id] = []
  })

  new Vue({
    el: '#statusboard',
    data: {
      checks: checks,
      checksData: checksData
    },
    computed: {
      statuses: function () {
        let s = {}
        Object.keys(this.checksData).forEach(id => {
          const last = this.checksData[id][0] || {}
          if (last.statusCode === 200 && !last.error) {
            s[id] = 'OK'
          } else if (last.statusCode >= 400 || last.error) {
            s[id] = 'error'
          } else {
            s[id] = 'waiting...'
          }
        })
        return s
      }
    }
  })

  // d3 charts

  const CHART_WINDOW_MINUTES = 60
  const chartMargins = { top: 10, right: 10, bottom: 40, left: 50 }

  let graphsMap = new Map()

  config.checks.forEach(check => {
    const id = check.id

    const numDataPoints = CHART_WINDOW_MINUTES * 60 / check.interval

    const elem = d3.select(`#graph-${id}`)
    const graph = elem.append('svg:svg')
        .attr('width', '100%')
        .attr('height', '150px')
      .append('g')
        .attr('transform', `translate(${chartMargins.left},${chartMargins.top})`)

    const elemDims = elem.node().getBoundingClientRect()
    const width = elemDims.width - chartMargins.left - chartMargins.right
    const height = elemDims.height - chartMargins.top - chartMargins.bottom

    const x = d3.scaleLinear()
      .domain([0, CHART_WINDOW_MINUTES])
      .range([width, 0])
    const y = d3.scaleLinear()
      .domain([0, check.timeout])
      .range([height, 0])

    const line = d3.line()
      .curve(d3.curveMonotoneX)
      .x((d, i) => x(i * check.interval / 60))
      .y(d => y(d))

    const initialData = [0]

    graph.append('svg:path')
      .attr('d', line(initialData))

    // x-axis
    graph.append('svg:g')
        .attr('class', 'axis x-axis')
        .attr('transform', `translate(0,${height})`)
        .call(d3.axisBottom(x).ticks(6))
      .append('text')
        .attr('class', 'axis-label')
        .text('minutes')
        .attr('transform', `translate(${width / 2}, 30)`)

    // y-axis
    graph.append('svg:g')
        .attr('class', 'axis y-axis')
        .call(d3.axisLeft(y).ticks(5))
      .append('text')
        .attr('class', 'axis-label')
        .text('ms')
        .attr('transform', `translate(-30,${height / 2})`)

    graphsMap.set(id, { graph, line, x, y })
  })

  // Server-sent events

  const source = new EventSource('/events')

  source.onmessage = e => {
    const check = JSON.parse(e.data) || {}
    const numDataPoints = CHART_WINDOW_MINUTES * 60 / check.interval

    // update data and graph
    checksData[check.id].unshift(check)

    const graphObj = graphsMap.get(check.id)
    const graph = graphObj.graph
    const line = graphObj.line
    const x = graphObj.x
    const y = graphObj.y

    const data = checksData[check.id].map(check => check.responseTime)

    graph.selectAll('path')
      .interrupt()
      .data([data])
      .classed('error', !!checksData[check.id][0].error)
      .attr('d', line)

    // limit to last numDataPoints
    if (checksData[check.id].length > numDataPoints) {
      checksData[check.id].pop()
    }
  }
})
