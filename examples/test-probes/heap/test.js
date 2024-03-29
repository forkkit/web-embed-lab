
/**
HeapProbe is a test probe that tests heap memory sizes.
It uses data in window._welHeapMemoryData which is provided by the prober-extension
*/
class HeapProbe {
	constructor() {
		console.log('Heap probe constructed')
	}

	/**
	@return {Object} data collected when the target embed script *is not* loaded
	@return {Object.success} true if the data collection was successful
	@return {Object.heapMemoryData} total number of throws exceptions
	*/
	async gatherBaselineData(){
		console.log('Heap baseline')
		await this._requestAndWaitForHeapMemory()
		const heapMemoryData = this._latestHeapMemoryData()
		return {
			success: heapMemoryData !== null,
			heapMemoryData: heapMemoryData
		}
	}

	/**
	@return {Object} the results of the probe
	@return {Object.passed}
	@return {Object.heapMemoryData}
	*/
	async probe(basis, baseline) {
		console.log('Probing heap')

		try {
			const result = {
				passed: true,
				heapMemoryData: null
			}

			if(!basis || Object.keys(basis).length == 0) {
				result.passed = true
				return result
			}

			await this._requestAndWaitForHeapMemory()
			result.heapMemoryData = this._latestHeapMemoryData()

			if(result.heapMemoryData === null){
				result.passed = false
				result.error = 'No heap memory data found.'
				return result
			}

			if(result.heapMemoryData === null){
				console.error('No heap memory data')
				result.passed = false
				return result
			}

			for(let key of Object.keys(basis)) {
				if(key === 'relative') continue
				const individualPass = this._testHeapMemoryKey(key, basis[key])
				if(individualPass === false){
					result.passed = false
				}
			}

			if(typeof basis.relative === 'undefined'){
				return result
			} 

			for(let key of Object.keys(basis.relative)){
				const individualPass = this._testHeapMemoryKey(key, basis.relative[key], baseline[key])
				if(individualPass === false){
					result.passed = false
				}
			}

			return result
		} catch (e) {
			console.error('Heap probe error: ' + e + ' ' + e.lineNumber)
			return {
				passed: false,
				error: 'Error: ' + e
			}
		}
	}

	_testHeapMemoryKey(key, basis, baseline=undefined){
		const latestValue = this._latestHeapMemoryDataValue(key)
		console.log('testing: ' + key + ' ' + latestValue + ' ' + basis)
		if(baseline !== undefined) console.log('with baseline', baseline)
		if(latestValue === null){
			console.error('Heap memory test key does not exist', key)
			return false
		}
		return window.__welValueMatches(latestValue, basis, baseline)
	}

	_latestHeapMemoryData(){
		if(!window._welHeapMemoryData || window._welHeapMemoryData.length === 0) return null
		return window._welHeapMemoryData[window._welHeapMemoryData.length - 1]
	}

	_latestHeapMemoryDataValue(key){
		const latestMemoryData = this._latestHeapMemoryData()
		if(latestMemoryData === null) return null
		if(typeof latestMemoryData[key] === 'undefined') return null
		return latestMemoryData[key]
	}

	async _requestAndWaitForHeapMemory(){
		window._welHeapMemoryData = []
		window.postMessage({ action: 'relay-to-background', subAction: 'snapshot-heap' }, '*')
		let waitsRemaining = 25
		let waitMilliseconds = 500
		while(window._welHeapMemoryData.length == 0 && waitsRemaining > 0){
			waitsRemaining -= 1
			await window.__welWaitFor(waitMilliseconds)
		}
	}
}

window.__welProbes["heap"] = new HeapProbe();
