import { User } from '../../User.ts'
import { Request, Response } from '../../../../framework/http.ts'

/**
 * Defines the MainController.
 *
 * @export
 * @controller MainController
 */
export namespace MainController {

    /**
     * Index page for the controller.
     *
     * @export
     * @returns {Response}
     */
    export function index(): Response {
        return `
            <h1>GET Form example</h1>
            <form action="/data" method="GET">
                <input type="text" name="name" placeholder="name">
                <input type="text" name="email" placeholder="email">
                <input type="submit">
            </form>
            <h1>POST Form example</h1>
            <form action="/data" method="POST">
                <input type="text" name="name" placeholder="name">
                <input type="text" name="email" placeholder="email">
                <input type="submit">
            </form>
        `
    }

    /**
     * Example data to post to.
     *
     * @export
     * @param {Request} { request }
     * @returns {Response}
     */
    export function data({ request }: Request): Response {
        const attributes = request.validate({
            'name': 'required',
            'email': 'required'
        })
        return { method: request.method(), ...attributes }
    }

    /**
     * Another page for the controller.
     *
     * @export
     * @returns {Response}
     */
    export function another(): Response {
        return { table: (new User).getTable() }
    }

    /**
     * A page with two parameters.
     *
     * @export
     * @param {Request} { params }
     * @returns {Response}
     */
    export function book({ params }: Request): Response {
        return `<h1>Book: ${params.name}, page ${params.page}</h1>`
    }

}
